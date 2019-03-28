package pdftext

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
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
	threads       = flag.IntP("threads", "c", 0, "Count of concurrent threads for processing files")

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
		var alltags = make(map[string]*OutputTag)
		var wg sync.WaitGroup
		var mtx sync.Mutex
		if *threads == 0 {
			*threads = (runtime.NumCPU() + 1) / 2
		}
		tagChan := make(chan OutputTag, *threads)
		for i := 0; i < *threads; i++ {
			tagChan <- OutputTag{}
		}
		filepath.Walk(*dir, func(path string, info os.FileInfo, err error) error {
			var reterr error
			if info.IsDir() == true {
				return reterr
			}
			if !strings.HasSuffix(path, ".pdf") {
				return reterr
			}
			oldTag := <-tagChan
			var tag = OutputTag{
				OriginalPDF: path,
				Output:      *output,
				WG:          &wg,
				tagChan:     tagChan,
				Mutex:       &mtx,
			}
			wg.Add(1)
			go tag.Process()
			oldTag.Extract(alltags)

			return nil
		})
		wg.Wait()
		for i := 0; i < *threads; i++ {
			oldTag := <-tagChan
			oldTag.Extract(alltags)
		}

		fmt.Println("Writing tags.json")
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

// Extract blah
func (tag *OutputTag) Extract(alltags map[string]*OutputTag) {
	if tag.OriginalPDF != "" {
		if tag.AddToAllTags {
			alltags[filepath.Base(tag.OriginalPDF)] = tag
		}
		if !tag.Renamed {
			for _, w := range tag.Words {
				if unicode.IsLetter([]rune(w)[0]) {
					w = strings.ToLower(w)
					c := allWords[w]
					allWords[w] = c + 1
				}
			}
		}
	}
}

// Process a tag
func (tag *OutputTag) Process() {
	defer func() {
		tag.tagChan <- *tag
		tag.WG.Done()
	}()
	var err error
	tag.Tags = make(map[string]bool)
	path := tag.OriginalPDF
	base := filepath.Base(path)
	textf := filepath.Join(tag.Output,
		strings.Replace(base, ".pdf", ".txt", 1))
	tag.TextFileName = textf
	// Is this a strict timestamp name?
	isNumericName := numericName.MatchString(path)
	pdf := filepath.Join(*output, base)
	tag.NewPDF = pdf
	if isNumericName || !*renameNewOnly {
		tag.processFileAndRename()
		tag.renameBase()
	}

	match := filepath.Base(tag.NewPDF) == filepath.Base(path)
	// If we want to write the text file as well
	if *writetext && (!*tagcvtonly || match) && isNumericName {
		err = ioutil.WriteFile(tag.TextFileName, []byte(tag.Text), os.ModePerm)
		if err != nil {
			log.Fatalln("writing", tag.TextFileName, err)
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
		tag.AddToAllTags = true
	}

}
func (tag *OutputTag) processFileAndRename() {
	// Get the text from the PDF file
	start := time.Now()
	path := tag.OriginalPDF
	text := processFile(path)
	tag.Text = text
	dur := time.Now().Sub(start)
	if dur > time.Millisecond*500 {
		fmt.Println(path, dur)
	}
	tag.FirstDate = findFirstDate(text)

	words := strings.Fields(text)
	for _, w := range words {
		w = strings.ToLower(w)
		runes := []rune(w)
		i := len(runes) - 1
		for ; i >= 0; i-- {
			r := runes[i]
			if unicode.IsDigit(r) || unicode.IsLetter(r) {
				break
			}
		}
		if i < 1 {
			continue
		}
		runes = runes[:i+1]
		firstRune := runes[0]
		if !unicode.IsDigit(firstRune) && !unicode.IsLetter(firstRune) {
			continue
		}
		tag.Words = append(tag.Words, string(runes))

	}
	seen := make(map[string]bool)
	j := 0
	for _, w := range tag.Words {
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = true
		tag.Words[j] = w
		j++
	}
	tag.Words = tag.Words[:j]
	sort.Strings(tag.Words)

	// Look for keywords in the text
	lctext := strings.ToLower(text)
	for k := range keywords {
		if strings.Contains(lctext, k) {
			tag.Tags[k] = true
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

	for k, v := range renameKeys {
		if len(v) == 1 {
			vp := strings.Split(v[0], "-")
			v = append(v, vp...)
			renameKeys[k] = v
		}
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
	OriginalPDF  string          // Path to original pdf file
	Output       string          // Output path directory
	NewPDF       string          // Name of new pdf file
	TextFileName string          // Name of text file name
	FirstDate    string          // If we found a date, it's here in 'Jan 2 2006' format
	Text         string          // The text from the file
	Tags         map[string]bool // What keywords were found in this file
	Words        []string        // Words found
	Renamed      bool            // Was there renaming
	AddToAllTags bool            // Tags should be added to composite
	WG           *sync.WaitGroup // The WaitGroup to use
	tagChan      chan OutputTag  // Control channel
	Mutex        *sync.Mutex     // Interlock to newfilenames
}

var keywords = make(map[string]bool)

var allWords = make(map[string]int)

func (tag *OutputTag) renameBase() {
	var newbase string

	convert := func(path *string) {
		dir := filepath.Dir(*path)
		*path = filepath.Join(dir,
			newbase+filepath.Ext(*path))
	}
	if len(tag.Tags) > 0 {
		// renamekeys is read-only
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
			// We are still "success" if we did not have a miss looking up the tags
			if success {
				// Renumber file names if necessary
				tag.Mutex.Lock()
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
				convert(&tag.TextFileName)
				convert(&tag.NewPDF)
				tag.Renamed = true
				newFileNames[newbase] = true
				tag.Mutex.Unlock()
				break
			}
		}
	} else {
		// If no text associated with file, sad.
		// Prepend "notext-" to the original name
		// Otherwise, no renaming happens
		if len(tag.Text) == 0 {
			base := filepath.Base(tag.OriginalPDF)
			ext := filepath.Ext(tag.OriginalPDF)
			newbase = "notext-" + strings.Replace(base, ext, "", 1)

			convert(&tag.TextFileName)
			convert(&tag.NewPDF)
		}
	}
}
