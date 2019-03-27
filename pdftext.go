package pdftext

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	flag "github.com/spf13/pflag"
	"rcs.io/pdf"
)

var (
	//	debug = flag.BoolP("debug", "d", false, "Print only uncategorized lines")
	dir           = flag.StringP("dir", "d", ".", "Descend into this directory")
	output        = flag.StringP("output", "o", "", "Place PDFs and text in this directory")
	files         = flag.StringArrayP("files", "f", []string{}, "Access these specific files")
	writetext     = flag.BoolP("text", "t", false, "Print recovered text")
	renameNewOnly = flag.BoolP("renamenew", "r", true, "Rename only new timestamp filenames")
	debug         = flag.BoolP("debug", "b", false, "Debug")
	symlink       = flag.BoolP("symlink", "s", false, "Create symlink to original PDF")
	lines         = flag.IntSliceP("line", "l", []int{}, "Lines to show in debug")
	tagcvtonly    = flag.BoolP("tagonly", "n", true, "Write tags only for unmatched PDFs")

	linemap      = make(map[int]bool)
	newFileNames = make(map[string]bool)
)

// Parse the options. Use of the 'files' flag overrides the 'dir' scan
// For files, range of the file list and invoke processFile
var preexistingFiles []string
var numericName = regexp.MustCompile(`\d\d\d\d(_\d\d){5}.pdf$`)

var start time.Time

func elapsed() time.Duration {
	return time.Now().Sub(start)
}

// Run does everything
func Run() {
	start = time.Now()
	flag.Parse()
	for _, i := range *lines {
		linemap[i] = true
	}
	if len(*files) > 0 {
		for _, file := range *files {
			// Ignore the returned tag info here.
			alltext := processFile(file)
			findFirstDate(alltext)
			if *writetext {
				fmt.Println(file)
				if *debug != true {
					fmt.Println(alltext)
				}

			}
		}
	} else {
		preexistingFiles, err := filepath.Glob("*pdf")
		if err != nil {
			log.Fatalln(err)
		}
		for _, f := range preexistingFiles {
			newFileNames[strings.Replace(filepath.Base(f), ".pdf", "", 1)] = true
		}
		var alltags = make(map[string]OutputTag)
		index := 0
		filepath.Walk(*dir, func(path string, info os.FileInfo, err error) error {
			var reterr error
			if info.IsDir() == true {
				return reterr
			}
			if !strings.HasSuffix(path, ".pdf") {
				return reterr
			}
			var tag OutputTag
			tag.Tags = make(map[string]bool)

			base := filepath.Base(path)
			textf := filepath.Join(*output,
				strings.Replace(base, ".pdf", ".txt", 1))
			tag.OriginalPDF = path
			tag.ExtractedText = textf
			// Is this a strict timestamp name?
			words := []string{}
			text := ""
			isNumericName := numericName.MatchString(path)
			pdf := filepath.Join(*output, base)
			tag.NewPDF = pdf
			if isNumericName || !*renameNewOnly {
				// Get the text from the PDF file
				start := time.Now()
				text = processFile(path)
				dur := time.Now().Sub(start)
				if dur > time.Millisecond*500 {
					fmt.Println(index, path, dur)
				}
				words = strings.Fields(text)
				tag.FirstDate = findFirstDate(text)

				// Look for keywords in the text
				lctext := strings.ToLower(text)
				for k := range keywords {
					if strings.Contains(lctext, k) {
						tag.Tags[k] = true
					}
				}
				renameBase(&tag, text)
			}
			index++

			if !tag.Renamed {
				for _, w := range words {
					if unicode.IsLetter([]rune(w)[0]) {
						w = strings.ToLower(w)
						c := allWords[w]
						allWords[w] = c + 1
					}
				}
			}
			match := filepath.Base(tag.NewPDF) == filepath.Base(path)
			// If we want to write the text file as well
			if *writetext && (!*tagcvtonly || match) && isNumericName {
				err = ioutil.WriteFile(tag.ExtractedText, []byte(text), os.ModePerm)
				if err != nil {
					log.Fatalln("writing", tag.ExtractedText, err)
				}
			}
			// If we want to write symlinks to original
			if *symlink {
				err := os.Symlink(path, tag.NewPDF)
				if err != nil {
					log.Fatalln("symlink", path, tag.NewPDF, err)
				}
			} else {
				// Otherwise write new PDF file
				bytes, err := ioutil.ReadFile(path)
				if err != nil {
					log.Fatalln("reading", path, err)
				}
				err = ioutil.WriteFile(tag.NewPDF, bytes, os.ModePerm)
				if err != nil {
					log.Fatalln("writing", tag.NewPDF, err)
				}
			}

			if (!*tagcvtonly || match) && isNumericName {
				alltags[base] = tag
			}
			return nil
		})
		// Create the JSON tag file
		bytes, err := json.MarshalIndent(alltags, " ", "")
		if err != nil {
			log.Fatalln(err)
		}
		err = ioutil.WriteFile(filepath.Join(*output, "tags.json"), bytes, os.ModePerm)
		if err != nil {
			log.Fatalln(err)
		}
		// Create the word count file
		for k, v := range allWords {
			if v < 4 || len(k) < 5 {
				delete(allWords, k)
			}
		}
		bytes, err = json.MarshalIndent(allWords, " ", "")
		if err != nil {
			log.Fatalln(err)
		}
		err = ioutil.WriteFile(filepath.Join(*output, "words.json"), bytes, os.ModePerm)
		if err != nil {
			log.Fatalln(err)
		}

	}

}

func processFile(file string) string {
	var tags OutputTag
	pw := func() string {
		return ""
	}
	tags.OriginalPDF = file
	tags.NewPDF = file

	f, err := os.Open(file)
	defer f.Close()
	if err != nil {
		log.Fatal(err)
	}
	st, err := f.Stat()
	if err != nil {
		log.Fatal(err)
	}

	defer func() {
		if r := recover(); r != nil {
			fmt.Println(">>>>>>>>> Panic recovery for", file, r)
		}
	}()
	r, err := pdf.NewReaderEncrypted(f, st.Size(), pw)
	if err != nil {
		if err == pdf.ErrInvalidPassword {
			log.Fatal("password not found")
		}
		log.Println("error reading", file, err)
	}
	//	fmt.Printf("%#v\n", r.Trailer())
	numpages := r.NumPage()
	//	fmt.Println("Pages:", numpages)
	var pages []string

	for i := 1; i <= numpages; i++ {
		//		fmt.Println(i)
		page := r.Page(i)
		pageText := getText(&page)
		pStrings := strings.Split(pageText, " ")
		var strCount, goodCount int
		for _, str := range pStrings {
			rune := []rune(str)
			strCount++
			if len(rune) > 0 {
				if unicode.IsDigit(rune[0]) || unicode.IsLetter(rune[0]) {
					goodCount++
				}
			}
		}
		if goodCount > strCount/2 {
			pages = append(pages, pageText)
		}
		//		fmt.Println()
	}
	input := strings.Join(pages, "\n")
	alltext := make([]rune, len(input))
	i := 0
	for _, c := range input {
		if utf8.RuneLen(c) == 1 && c != 0 {
			alltext[i] = c
			i++
		}
	}
	return string(alltext)
}

// Given a Page, return a string containing the best guess of the white space separation for
// the Text elements in the page.
// 1. Successive Text elememts with a difference in Y coordinate of less than half the size of the font
//  are presumed to be on the same line. Otherwise, a newline character is interest
// 2. If the successive characters are on the same line, if the new character is more than 1.5 the width
// of the prior character after the prior character, add a space.
// 3. Add the new character, then finally return the aggregated string.
func getText(page *pdf.Page) string {
	var prevText pdf.Text
	first := 0
	alltext := page.Content().Text
	var ret string
	var dout string
	lineno := 0
	for idx, t := range alltext {
		newLine := false
		var dx, dy float64
		if prevText.Font != "" {
			dx = t.X - prevText.X
			dy = t.Y - prevText.Y
		}
		newLine = -dy > (prevText.FontSize / 5.0)
		if newLine == false {
			if dx > 1.5*prevText.W {
				//ret += fmt.Sprint(" ")
			}
		} else {
			lineno++
			if idx == 0 {
				prevText = t
				continue
			}

			ret += fmt.Sprintln()
			// Attempt to accomodate recreating spaces by
			// interpreting the spacing between
			// successive characters on a line.
			if idx > first+1 {
				var dxs []float64
				for i := first; i < idx; i++ {
					here := alltext[i]
					gap := 0.0
					if i > 0 {
						gap = here.X - alltext[i-1].X - alltext[i-1].W
					}
					if i > first {
						dxs = append(dxs, gap)
					}
					if *debug {
						if len(linemap) == 0 || linemap[lineno] == true {
							fmt.Printf("%.1f/%.1f/%.1f/%s ",
								here.FontSize,
								here.X, gap, here.S)
						}
					}
				}
				sort.Float64s(dxs)
				mid := int(len(dxs)) / 2
				median := dxs[mid]
				if median < 0.0 {
					median = 0.0
				}
				ret += fmt.Sprint(alltext[first].S)
				if *debug {
					dout += fmt.Sprint(alltext[first].S)
				}

				var prior pdf.Text

				for i := first; i < idx; i++ {
					now := alltext[i]
					if i == first {
						prior = now
						continue
					}
					gap := now.X - prior.X - prior.W
					if gap > median+prior.FontSize/5.0 ||
						now.Font != prior.Font {
						//						now.FontSize != prior.FontSize {
						if *debug {
							dout += fmt.Sprint(" ")
						}
						ret += fmt.Sprint(" ")
					}
					if *debug {
						dout += fmt.Sprint(now.S)
					}
					ret += fmt.Sprint(now.S)
					prior = now
				}
				if *debug && (len(linemap) == 0 || linemap[lineno]) {
					fmt.Println(dout)
					dout = ""
				}

			}

			first = idx

		}
		prevText = t
	}
	return ret
}

var monthRE string
var dateRE *regexp.Regexp
var expRE1 []*regexp.Regexp

// REs to determine where i=to insert a space in the date if necessary
var expStrs = []string{
	`(\d)([[:alpha:]])`,
	`([[:alpha:]])(\d)`,
	`(\d+)(\d{4})`,
}

func init() {
	var months []string
	for m := time.January; m <= time.December; m++ {
		months = append(months, m.String())
		months = append(months, m.String()[:3])
	}
	monthRE := "(" + strings.Join(months, "|") + ")"
	//	fmt.Println(mstr)
	//	monthRE = regexp.MustCompile(mstr)
	dateRE = regexp.MustCompile(`(?i)((` + monthRE + `\d\d?|\d\d?` + monthRE + `),?(19|20)\d{2}|\d\d?\/\d\d?\/(19|20)?(\d\d)|20\d\d\/\d\d/\\d\d)`)

	for _, restr := range expStrs {
		expRE1 = append(expRE1, regexp.MustCompile(restr))
	}

	for _, v := range renameKeys {
		for i, name := range v {
			if i == 0 {
				continue
			}
			lcname := strings.ToLower(name)
			v[i] = lcname
			keywords[lcname] = true
		}
	}
}

var dateFormats = []string{
	"Jan 2 2006",
	"2 Jan 2006",
	"January 2 2006",
	"2 January 2006",
	"1/2/06",
	"1/2/2006",
	"2006/1/2",
}

// Find the first plausible date string in the text. Convert it to a consistent
// date string and return that
func findFirstDate(text string) string {

	dateFind := func(text string) string {
		var cvt time.Time
		var err error
		date := dateRE.FindString(text)
		dateOut := ""
		if date != "" {
			//			date = date[:len(date)-1]
			date = strings.ToLower(strings.Replace(date, ",", "", 1))
			// Rehydrate spaces in the date string
			for _, re := range expRE1 {
				date = re.ReplaceAllString(date, "$1 $2")
			}
			// Look for a format that we parse correctly
			for _, fmt := range dateFormats {
				cvt, err = time.Parse(fmt, date)
				if err == nil {
					dateOut = cvt.Format("-2006-Jan-2")
					break
				}
			}
		}
		return dateOut
	}

	date := dateFind(text)
	if date == "" {
		t1 := strings.Replace(text, " ", "", -1)
		date = dateFind(t1)
	}
	return date
}

// OutputTag does
type OutputTag struct {
	OriginalPDF   string
	NewPDF        string
	ExtractedText string
	FirstDate     string          // If we found a date, it's here in 'Jan 2 2006' format
	Tags          map[string]bool // What keywords were found in this file
	Renamed       bool            // Was there renaming
}

var keywords = make(map[string]bool)

var allWords = make(map[string]int)

var renameKeys = [][]string{
	[]string{"Friends-Forest", "friends", "forest"},
	[]string{"Vanguard-1099DIV", "valley forge", "1099-div"},
	[]string{"McHugh-Law", "mchugh law"},
	[]string{"1099-HC", "1099-hc"},
	[]string{"NRWA", "nashua", "river", "watershed", "association"},
	[]string{"NHLakes", "nh lakes"},
	[]string{"ACLU", "american civil liberties union"},
	[]string{"AdaFruit", "www.adafruit.com"},
	[]string{"ChelmsfordPC-Jim", "chelmsford primary care"},
	[]string{"CGLIC", "lincoln national"},
	[]string{"WBUR", "membership@wbur.org"},
	[]string{"Packing-List", "packing list"},
	[]string{"Rindge-Yard", "scenic", "landscaping"},
	[]string{"Rindge-Yard", "sc enic", "landscaping"},
	[]string{"Rindge-Delinq-Tax", "rindge", "delinquent"},
	[]string{"Family-Eyecare", "family eye care"},
	[]string{"Nepenthe-Foothills", "foothills property"},
	[]string{"Nepenthe-Foothills", "liverez"},
	[]string{"Groton-Insurance", "cambridge mutual", "2493319"},
	[]string{"Rindge-Insurance", "cambridge mutual", "2526086"},
	[]string{"Rindge-Insurance", "chase", "durand"},
	[]string{"Jim-LifeInsur", "administrator group"},
	[]string{"Jim-LifeInsur", "administrator", "ieee"},
	[]string{"Steward-Medical", "steward health care"},
	[]string{"Steward-Medical", "steward medical"},
	[]string{"Rindge-Water", "jaffrey", "water"},
	[]string{"Nature-Conservancy", "nature", "conservancy"},
	[]string{"Auto-Excise", "groton", "excise"},
	[]string{"Rindge-Tax", "town of rindge", "tax collector"},
	[]string{"Nepenthe-NHOA", "nhoa"},
	[]string{"Groton-Water", "groton water department"},
	[]string{"CGLIC", "whole life"},
	[]string{"Rindge-Telephone", "fairpoint"},
	[]string{"AmericanForests", "american forests"},
	[]string{"Groton-Heating", "spadafore", "oil"},
	[]string{"Groton-Heating", "wilson", "hvac"},
	[]string{"NetApp", "netapp"},
	[]string{"NVMC", "nashoba valley med"},
	[]string{"CongShalom", "congregation shalom"},
	[]string{"CongShalom", "congregationshalom.org"},
	[]string{"Delicate-Yard", "willow", "landscape"},
	[]string{"Rindge-Generator", "powers generator"},
	[]string{"Rindge-Door", "overhead door company of concord"},
	[]string{"Costco-Visa", "costco anywhere visa"},
	[]string{"Cuisinart", "cuisinart"},
	[]string{"Nepenthe-Electric", "aps", "energy", "arizona"},
	[]string{"Nepenthe-Electric", "aps.com"},
	[]string{"Sedona-Sewer-Past-Due", "Roadrunner", "past due", "Sedona"},
	[]string{"Nepenthe-Water", "arizona", "water"},
	[]string{"Sedona-Tax", "yavapai", "treasurer"},
	[]string{"Nepenthe-Tax", "yavapai", "treasurer", "nepenthe"},
	[]string{"Nepenthe-Tax-Refund", "yavapai", "treasurer", "refund"},
	[]string{"Nepenthe-Notice-Value", "yavapai", "assessor", "nepenthe"},
	[]string{"Buckboard-Notice-Value", "yavapai", "assessor", "hills"},
	[]string{"Buckboard-HOA", "western", "hills", "property", "owner"},
	[]string{"Groton-Tax", "town of groton", "real estate", "tax"},
	[]string{"Groton-Water", "grotonwater"},
	[]string{"Medical-Podiatrist", "jeffrey", "resnick"},
	[]string{"Fios", "fios"},
	[]string{"Auto-Service", "wiisonsservicegroton"},
	[]string{"Rindge-Alarm", "central", "monitoring", "service"},
	[]string{"Motion-FCU", "federal credit union"},
	[]string{"Optum-HSA", "optum bank"},
	[]string{"Loaves-Fishes", "loaves & fishes"},
	[]string{"Jim-Longterm", "columbus life"},
	[]string{"Medical-DrH", "harasimowicz"},
	[]string{"Groton-Fuel", "heating oil"},
	[]string{"Groton-Fuel", "eastern propane", "diesel"},
	[]string{"Groton-Fuel", "800", "696", "0432", "diesel"},
	[]string{"Medical-Reilly", "baptist", "hospital"},
	[]string{"Nashoba-Radiology", "nashoba radiology"},
	[]string{"Nashoba-Pathology", "nashoba", "valley", "pathology"},
	[]string{"Auto-Registration", "massachusetts vehicle registration renewal"},
	[]string{"Citizens-Bank", "citizens drive"},
	[]string{"Citizens-Bank", "citizens bank"},
	[]string{"Netapp-Stock", "netapp", "stock"},
	[]string{"BofA", "bank of america"},
	[]string{"NetApp", "network appliance"},
	[]string{"Check", "features", "exceed industry"},
	[]string{"Check", "do not write, stamp or sign below this line"},
	[]string{"Rindge-Electric", "eversource"},
	[]string{"Groton-Electric", "groton", "electric", "light"},
	[]string{"Rindge-Pest", "pest", "services", "rindge"},
	[]string{"Rindge-Pest", "pest", "services", "florence"},
	[]string{"Rindge-Pest", "pest", "management", "florence"},
	[]string{"Groton-Pest", "montachusett", "pest"},
	[]string{"Anelons-Chiro", "terry", "anelons"},
	[]string{"Rindge-Reassessment", "new assessment", "rindge"},
	[]string{"Nepenthe-Insurance", "State Farm"},
	[]string{"Buckboard-Insurance", "Shermantine"},
	[]string{"Subaru-Loan", "subaru motors finance"},
	[]string{"Epilepsy-Foundation", "Epilepsy", "Foundation"},
	[]string{"Rindge-Belletetes", "Lumber Barns"},
	[]string{"Rindge-Belletetes", "Belletetes"},
	[]string{"Nashoba-Family-Med", "nashoba family med"},
	[]string{"Mass-1099G", "1099-G", "Massachusetts"},
	[]string{"Mass-DOR-Assessment", "Intent", "Assess", "Massachusetts"},
	[]string{"Home-Depot", "More saving", "More doing"},
	[]string{"Morgan-Stanley", "morgan", "stanley"},
	[]string{"Home-Depot.com", "homedepot.com"},
	[]string{"CLAPA", "contoocook lake", "preservation"},
	[]string{"CLAPA", "contoocook lake", "assoc"},
	[]string{"Quest-Labs", "quest diagnostics"},
	[]string{"Auto-Repair", "wilsonservice"},
	[]string{"Dental", "peter", "breen"},
	[]string{"EFTPS", "EFTPS"},
	[]string{"Sharon-ENT", "Ear", "Nose", "Throat"},
	[]string{"Diving", "Northeast", "Scuba"},
	[]string{"Habitat-Humanity", "habitat", "humanity"},
	[]string{"Brown-Insurance", "brown", "pepperell"},
	[]string{"HPHC", "harvard pilgrim"},
	[]string{"UHC", "united", "health", "care"},
	[]string{"Audi-Nashua", "audi", "nashua"},
	[]string{"Auto-Insurance", "commerce", "insurance"},
	[]string{"BIDMC-Medical", "beth", "israel", "deaconess"},
	[]string{"BIDMC-Medical", "associated", "physicians", "bidmc"},
	[]string{"Boundaries-Therapy", "Boundaries", "Therapy"},
	[]string{"Boat-Insurance", "foremost", "insurance"},
	[]string{"Myette", "myette"},
	[]string{"Abby-Hall-Scholarship", "abby", "hall"},
	[]string{"ETrade", "E*trade"},
	[]string{"Village-Subaru", "village", "subaru"},
	[]string{"Nashua-Subaru", "nashua", "subaru"},
	[]string{"Helfman-Lasky", "helfman", "lasky"},
	[]string{"Medical-Lab", "Emerson", "Pathology"},
	[]string{"Audi-America", "Audi", "America"},
	[]string{"Subaru-America", "Subaru", "America"},
	[]string{"Vanguard", "Vanguard", "Group"},
	[]string{"Boat-License", "boat", "renewal", "notice"},
	[]string{"Nashoba-ED", "steward", "emergency", "physicians"},
	[]string{"Rindge-Assoc", "woodmere association"},
	[]string{"Rindge-Fuel", "eastern", "propane", "rindge"},
	[]string{"Nepenthe-Gas", "unisource"},
	[]string{"CVS-Drugs", "CVS", "pharmacy"},
	[]string{"Buckboard-Sedona-Rentals", "buckboard", "sedona", "rentals"},
	[]string{"Delta-Dental", "delta", "dental"},
	[]string{"Rindge-Cable", "argent", "communications"},
	[]string{"AMEX", "american", "express"},
	[]string{"Goodwill", "goodwill", "industries"},
	[]string{"Citibank-CC", "citibank", "client", "services"},
	[]string{"Nepenthe-Telephone", "CenturyLink"},
	[]string{"Nepenthe-Telephone", "qwest"},
	[]string{"Circuit-City-Chase", "chase", "card", "services", "circuit", "city"},
	[]string{"Park-West", "Park", "west"},
	[]string{"Rindge-Electric", "PSNH"},
	[]string{"Rindge-Construction", "LAMPS", "PLUS"},
	[]string{"Rindge-Construction", "cushnie", "moore"},
	[]string{"Rindge-Construction", "james", "libby", "moore"},
	[]string{"Rindge-Construction", "selmer", "foundations"},
	[]string{"Groton-Cable", "charter", "cable"},
	[]string{"Eastern-Propane", "eastern", "propane"},
	[]string{"Acton-Toyota", "Acton", "Toyota"},
	[]string{"Nashua-Toyota", "Nashua", "Toyota"},
	[]string{"Sharon-SIMPLE", "Vanguard", "SIMPLE", "IRA"},
	[]string{"Sharon-SIMPLE", "Fidelity", "SIMPLE", "IRA"},
	[]string{"Sharon-SIMPLE2", "smith", "barney", "SIMPLE", "IRA"},
	[]string{"Sharon-Schwartz", "sharon", "richard", "schwartz"},
	[]string{"Jim-Schwartz", "james", "richard", "schwartz"},
	[]string{"Groton-Security", "adt", "security"},
	[]string{"Rindge-Security", "monadnock", "security"},
	[]string{"Rindge-Plumbing", "dupre", "plumbing"},
	[]string{"Smith-Barney", "smith", "barney"},
	[]string{"MVJF", "Merrimack", "Jewish", "Federation"},
	[]string{"Fidelity-401K", "Fidelity", "netbenefits"},
	[]string{"CongShalom-Donation", "enriched", "by", "donation"},
	[]string{"TelDrug", "TelDrug"},
	[]string{"Buckboard-Sewer", "Buckboard", "Sedona", "Roadrunner", "Wastewater"},
	[]string{"Nepenthe-Sewer", "Jasmine", "Sedona", "Roadrunner", "Wastewater"},
	[]string{"Nepenthe-Cable", "NPG", "CABLE"},
	[]string{"Nepenthe-Cable", "suddenlink"},
	[]string{"Reilly-ProSports", "pro", "sports", "ortho"},
	[]string{"Asbestos-Removal", "asbestos"},
	[]string{"UFund-Reid", "u.fund", "reid"},
	[]string{"UFund-Riley", "u.fund", "riley"},
	[]string{"Middlesex-Savings", "middlesex", "savings", "bank"},
	[]string{"Fidelity", "fidelity"},
	[]string{"Citizens'", "Climate"},
	[]string{"Tesla", "tesla"},
	[]string{"Consolidated-Communications", "consolidated", "communications"},
	[]string{"Check", "colored", "security", "background"},
	[]string{"IBM-W2", "ibm", "w-2", "earnings", "summary"},
	[]string{"IBM-W2", "business", "machines", "w-2", "wage", "tax"},
	[]string{"IBM-Overpayment", "ibm", "overpayment"},
	[]string{"NetApp-W2", "netapp", "w-2", "earnings", "summary"},
	[]string{"Clapa-Paypal", "paypal", "1099-k"},
	[]string{"CGLIC", "lincoln", "national", "life", "insurance"},
	[]string{"1095-C", "1095-C"},
	[]string{"Mass-Car-Title", "department", "transportation", "title"},
	[]string{"Yellowstone-Forever", "yellowstone", "forever"},
	[]string{"Lowell-General", "lowell", "general", "hospital"},
	[]string{"Staples", "staples"},
	[]string{"WageWorks", "wageworks"},
	[]string{"Groton-Council-Aging", "groton", "council", "aging"},
	[]string{"AAA", "aaa", "member"},
	[]string{"IRS", "department", "treasury", "internal", "revenue"},
	[]string{"CB-CD", "citizens", "bank", "cd", "statement"},
	[]string{"Anthem-Summary", "anthem", "health", "care", "summary"},
	[]string{"Anthem-BC", "anthem", "blue", "cross"},
	[]string{"Anthem-BCBS", "anthem", "blue", "cross", "shield"},
	[]string{"Sedona-Elite", "sedona", "elite", "properties", "mgmt"},
	[]string{"Hawaiian-Airlines", "hawaiian", "airlines"},
	[]string{"National-Parks", "national", "parks", "conservation"},
	[]string{"Tesla", "S 75D"},
	[]string{"Car-Warranty", "autoassure"},
	[]string{"Check", "pay", "order"},
	[]string{"Check", "pay", "to", "check"},
	[]string{"Check", "pay", "to", "endorse"},
	[]string{"Check", "pay", "o r d e"},
	[]string{"Caremark.com", "caremark.com"},
	[]string{"Wilsons-Service", "wilson's", "service", "center"},
	[]string{"Rindge-RE-Tax", "40 florence", "real estate", "tax"},
	[]string{"PJLibrary", "PJ", "library"},
	[]string{"Mass-Gun", "massachusetts", "firearms"},
	[]string{"Charitable", "charitable", "gift"},
	[]string{"IEEE-Membership", "IEEE", "Membership"},
	[]string{"Sedona-Landscaping", "Harbison", "sedona"},
	[]string{"Groton-Variance", "groton", "variance"},
	[]string{"Audi-Recall", "audi", "safety", "recall"},
	[]string{"IBM-401k", "ibm", "401", "account"},
	[]string{"Parker-School", "parker", "school"},
	[]string{"Northeast-Endoscopy", "northeast", "endoscopy"},
	[]string{"1099-SA", "form", "1099-sa"},
	[]string{"VVAC", "verde", "valley", "archaeology"},
	[]string{"LGH-Cardiology", "lgh", "merrimack", "cardiology"},
	[]string{"Redstone-Properties-Sedona", "redstone", "properties", "sedona"},
	[]string{"FastLane", "fast lane"},
	[]string{"CongShalom", "87 richardson"},
	[]string{"NetApp-CIGNA", "visalia", "claim"},
	[]string{"NetApp-CIGNA", "cigna"},
	[]string{"Sedona-Buckboard", "western hills"},
	[]string{"Murphy-Insurance", "dfmurphy.com"},
	[]string{"BankofAmerica", "BankofAmerica"},
	[]string{"Metlife-Dental", "metlife", "dental"},
	//
	[]string{"Tax-Deductible", "tax", "deductible"},
}

func renameBase(tag *OutputTag, text string) {
	var newbase string
	convert := func(path *string) {
		dir := filepath.Dir(*path)
		*path = filepath.Join(dir,
			newbase+filepath.Ext(*path))
	}
	if len(tag.Tags) > 0 {
		for _, arr := range renameKeys {
			newbase1 := arr[0]
			newbase = newbase1 + tag.FirstDate
			success := true
			for _, item := range arr[1:] {
				if _, ok := tag.Tags[item]; ok == false {
					success = false
					break
				}
			}
			if success {
				// Renumber file names if necessary
				if newFileNames[newbase] == true {
					for suffix := 1; true; suffix++ {
						nextName := fmt.Sprintf("%s-%d", newbase, suffix)
						if newFileNames[nextName] {
							continue
						}
						newbase = nextName
						break
					}
				}
				convert(&tag.ExtractedText)
				convert(&tag.NewPDF)
				tag.Renamed = true
				newFileNames[newbase] = true
				break
			}
		}
	} else {
		if len(text) == 0 {
			base := filepath.Base(tag.OriginalPDF)
			ext := filepath.Ext(tag.OriginalPDF)
			base = strings.Replace(base, ext, "", 1)
			newbase = "notext-" + base
			convert(&tag.ExtractedText)
			convert(&tag.NewPDF)
		}
	}
}
