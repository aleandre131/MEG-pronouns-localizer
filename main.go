// Localizer — persons vs places, image vs word (MEG, NeuroSpin).
//
// The 32 items of the main experiment (16 persons, 16 places, items.csv) are
// shown one at a time in the centre of the screen, in a square frame: 500 ms
// on, then 500 ms of the fixation dot. Image and word trials strictly
// alternate. On 20 trials per run the frame is red instead of white; the
// participant presses a green button as soon as they see it (vigilance task,
// unrelated to person/place). 2 runs of 384 trials (6.5 min each), in one
// session with a pause between them; each item appears 6 times as an image and
// 6 times as a word per run. The photos are the normalised ones
// (normalize_images.py), embedded in the binary.
//
// Trigger codes: see sequence.go (1-128 stimulus onsets, 200+run before a
// run, 255 end). Every code is written right after the flip that shows the
// stimulus, together with the photodiode square (top-left). The response
// buttons are recorded by the MEG on their own STI lines.
//
// Hardware, as in MEG-pronouns-main (Pushmi, NeuroSpin): triggers on the data
// lines of /dev/parport1 (STI001-STI008); GREEN buttons on status lines (rig
// table 19/10/2026): left = port 0 0b0001 (status bit 3, 0x08, STI009), right =
// port 1 0b0100 (status bit 5, 0x20, STI014). Either button counts.
//
// Usage:
//
//	go run . -w -s 1               # windowed test, keyboard (SPACE or arrows)
//	go run . -s 1 -meg             # MEG session
//	go run . -s 1 -meg -run 2      # start at run 2 (after a crash)
package main

import (
	"bytes"
	"embed"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chrplr/goxpyriment/apparatus"
	"github.com/chrplr/goxpyriment/assets_embed"
	"github.com/chrplr/goxpyriment/control"
	"github.com/chrplr/goxpyriment/results"
	"github.com/chrplr/goxpyriment/stimuli"
	"github.com/chrplr/goxpyriment/triggers"
)

//go:embed items.csv
var itemsCSV []byte

//go:embed assets/images/persons assets/images/places
var imagesFS embed.FS

// ---------------------------------------------------------------------------
// Timing (ms)

const (
	stimOnMs      = 500
	stimOffMs     = 500
	leadFixMs     = 2000 // fixation before the first trial of a run
	respWindowMs  = 1000 // a press up to this long after a red frame's onset is a hit
	endScreenMs   = 5000
	dataDir       = "data"
	pollInterval  = 500 * time.Microsecond
	triggerPort   = "/dev/parport1"
	leftBtnPort   = "/dev/parport0" // green left (STI009)
	rightBtnPort  = "/dev/parport1" // green right (STI014)
	leftBtnMask   = 0x08            // status bit 3 = expyriment read_status 0b0001
	rightBtnMask  = 0x20            // status bit 5 = expyriment read_status 0b0100
	frameSize     = 360 // outer side of the frame, px (photos are 320 x 320)
	frameWidth    = 4
	defaultFontPt = 48 // as the words of the main experiment
	fixRadius     = 4
)

var (
	backgroundColor = control.Black
	textColor       = control.White
	fixColor        = control.RGB(192, 192, 192) // as in the main experiment
	frameColor      = control.White
	targetColor     = control.RGB(255, 0, 0)
)

// ---------------------------------------------------------------------------
// Response buttons

// press is one button / key press, on the SDL clock.
type press struct {
	ns     uint64
	source string // "left", "right", "key"
}

// buttonReader polls the green buttons in the background while a run is
// presented, and records each press (transition to "down").
type buttonReader struct {
	left, right  *triggers.ParallelPort
	baseL, baseR byte
	mu           sync.Mutex
	presses      []press
	stop         chan struct{}
	done         sync.WaitGroup
	errs         int
}

func newButtonReader(left, right *triggers.ParallelPort) (*buttonReader, error) {
	b := &buttonReader{left: left, right: right}
	var err error
	if b.baseL, err = left.ReadStatus(); err != nil {
		return nil, err
	}
	if b.baseR, err = right.ReadStatus(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *buttonReader) start() {
	b.mu.Lock()
	b.presses = nil
	b.mu.Unlock()
	b.stop = make(chan struct{})
	b.done.Add(1)
	go func() {
		defer b.done.Done()
		var downL, downR bool
		first := true
		for {
			select {
			case <-b.stop:
				return
			default:
			}
			ts := control.TicksNS()
			sl, e1 := b.left.ReadStatus()
			sr, e2 := b.right.ReadStatus()
			if e1 != nil || e2 != nil {
				b.errs++
				time.Sleep(pollInterval)
				continue
			}
			dl := (sl^b.baseL)&leftBtnMask != 0
			dr := (sr^b.baseR)&rightBtnMask != 0
			if !first { // a button held when the run starts is not a press
				b.mu.Lock()
				if dl && !downL {
					b.presses = append(b.presses, press{ts, "left"})
				}
				if dr && !downR {
					b.presses = append(b.presses, press{ts, "right"})
				}
				b.mu.Unlock()
			}
			downL, downR, first = dl, dr, false
			time.Sleep(pollInterval)
		}
	}()
}

func (b *buttonReader) finish() []press {
	close(b.stop)
	b.done.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]press(nil), b.presses...)
}

// ---------------------------------------------------------------------------
// Texts (French)

func instructionText(meg bool) string {
	how := "appuyez sur ESPACE"
	if meg {
		how = "appuyez sur un bouton vert (gauche ou droit)"
	}
	return "Bienvenue !\n\n" +
		"Des photos et des mots vont apparaître l'un après l'autre au centre de l'écran, " +
		"dans un cadre blanc.\n\n" +
		"Regardez-les attentivement en gardant les yeux au centre.\n\n" +
		"De temps en temps, le cadre devient ROUGE : " + how + " dès que vous le voyez.\n\n" +
		"Évitez de cligner des yeux pendant que les images et les mots sont affichés."
}

func practiceDoneText(hits, n int) string {
	return fmt.Sprintf("Entraînement terminé.\n\nVous avez repéré %d cadre(s) rouge(s) sur %d.\n\n"+
		"L'expérience va commencer.", hits, n)
}

func pauseText(run int) string {
	return fmt.Sprintf("Pause\n\nPartie %d sur %d terminée.\n\nReposez-vous quelques instants.\n\n"+
		"Rappel : appuyez dès que le cadre devient ROUGE.", run-1, nRuns)
}

const endText = "C'est terminé.\n\nMerci pour votre participation !"

// ---------------------------------------------------------------------------

func main() {
	meg := flag.Bool("meg", false, "MEG mode: triggers on "+triggerPort+", answers with the green buttons (otherwise: keyboard, no triggers)")
	photodiode := flag.String("photodiode", "auto", "photodiode square in the top-left corner: on, off, or auto (on with -meg)")
	photodiodeSize := flag.Float64("photodiode-size", control.DefaultPhotodiodeSize, "side of the photodiode square, in pixels")
	ttlMs := flag.Float64("ttl-ms", 0, "how long each trigger code stays on the lines, in ms (0 = 3 frames)")
	startRun := flag.Int("run", 1, "run to start from (to resume an interrupted session)")
	practice := flag.Bool("practice", true, "16 practice trials (3 red frames, no triggers) before run 1")
	fontSize := flag.Float64("font-size", defaultFontPt, "point size of the words")
	auto := flag.Bool("auto", false, "start runs without SPACE and press at random on red frames (timing checks)")
	outDir := flag.String("o", dataDir, "folder for the data files; a relative path is taken from the project folder")
	nTrials := flag.Int("n", 0, "only the first N trials of each run (quick tests; 0 = all 384)")

	exp := control.NewExperimentFromFlags("Localizer", backgroundColor, textColor, 32,
		control.InfoField{Name: "output_dir", Label: "Results folder (relative to the Localizer folder)", Default: dataDir})
	defer exp.End()

	dir := *outDir
	if chosen := strings.TrimSpace(exp.Info["output_dir"]); chosen != "" && !flagGiven("o") {
		dir = chosen
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(projectDir(), dir)
	}
	if err := moveDataFiles(exp.Data, dir); err != nil {
		exp.Fatal("data folder: %v", err)
	}
	log.Printf("data will be saved to %s", exp.Data.FullPath)
	if *startRun < 1 || *startRun > nRuns {
		exp.Fatal("-run %d: must be between 1 and %d", *startRun, nRuns)
	}

	// Items and trials.
	photos, err := fs.Sub(imagesFS, "assets/images")
	if err != nil {
		exp.Fatal("%v", err)
	}
	items, err := loadItems(itemsCSV, photos)
	if err != nil {
		exp.Fatal("%v", err)
	}
	runs := make([][]*Trial, nRuns+1)
	for r := 1; r <= nRuns; r++ {
		if runs[r], err = buildRun(items, exp.SubjectID, r); err != nil {
			exp.Fatal("%v", err)
		}
		if *nTrials > 0 && *nTrials < len(runs[r]) {
			runs[r] = runs[r][:*nTrials]
		}
	}
	var practiceTrials []*Trial
	if *practice && *startRun == 1 {
		practiceTrials = buildPractice(items)
	}

	// Hardware.
	var buttons *buttonReader
	var ttlOut apparatus.MarkerOutput
	if *meg {
		trig := triggers.NewParallelPort(triggerPort)
		if err := trig.Open(); err != nil {
			exp.Fatal("trigger port: %v", err)
		}
		ttlOut = trig
		ports := map[string]*triggers.ParallelPort{triggerPort: trig}
		openPort := func(path string) *triggers.ParallelPort {
			if p, ok := ports[path]; ok {
				return p
			}
			p := triggers.NewParallelPort(path)
			if err := p.Open(); err != nil {
				exp.Fatal("button port %s: %v", path, err)
			}
			ports[path] = p
			return p
		}
		left, right := openPort(leftBtnPort), openPort(rightBtnPort)
		for path, p := range ports {
			if path != triggerPort {
				defer p.Close()
			}
		}
		if buttons, err = newButtonReader(left, right); err != nil {
			exp.Fatal("response buttons: %v", err)
		}
	}
	if *photodiode != "on" && *photodiode != "off" && *photodiode != "auto" {
		exp.Fatal("-photodiode %q: on, off or auto", *photodiode)
	}
	usePhotodiode := *photodiode == "on" || (*photodiode == "auto" && *meg)
	if usePhotodiode || ttlOut != nil {
		desc := ""
		if ttlOut != nil {
			desc = "parallel port " + triggerPort
		}
		exp.Marker = apparatus.NewMarker(apparatus.MarkerOptions{
			Photodiode: usePhotodiode, Size: float32(*photodiodeSize),
			Output: ttlOut, OutputDesc: desc,
			TTLDuration: time.Duration(*ttlMs * float64(time.Millisecond)),
		})
		exp.Screen.SetMarker(exp.Marker)
		if ttlOut != nil {
			if err := ttlOut.AllLow(); err != nil {
				exp.Fatal("trigger port: %v", err)
			}
		}
	}
	respDesc := "keyboard (SPACE or arrows)"
	if buttons != nil {
		respDesc = fmt.Sprintf("MEG green buttons, either: left %s mask 0x%02x, right %s mask 0x%02x (baselines 0x%02x, 0x%02x)",
			leftBtnPort, leftBtnMask, rightBtnPort, rightBtnMask, buttons.baseL, buttons.baseR)
	}
	exp.AddExperimentInfo("responses: " + respDesc)
	exp.AddExperimentInfo(fmt.Sprintf("triggers: %v; photodiode: %v (%.0f px, top-left); ttl duration: %v",
		ttlOut != nil, usePhotodiode, *photodiodeSize, exp.Marker.TTLDuration()))
	exp.AddExperimentInfo(fmt.Sprintf("design: %d runs x %d trials, %d/%d ms, %d red frames per run, response window %d ms; start run %d; first format run1=%s",
		nRuns, len(runs[1]), stimOnMs, stimOffMs, targetsPerRun, respWindowMs, *startRun, firstFormat(exp.SubjectID, 1)))
	log.Print("responses: " + respDesc)

	// Stimuli, all built and uploaded before the session.
	font, err := control.FontFromMemory(assets_embed.InconsolataFont, float32(*fontSize))
	if err != nil {
		exp.Fatal("font: %v", err)
	}
	fix := newDot(fixRadius, fixColor)
	if err := stimuli.PreloadVisualOnScreen(exp.Screen, fix); err != nil {
		exp.Fatal("fixation dot: %v", err)
	}
	pictures := map[string]*stimuli.Picture{}
	words := map[int]*stimuli.TextLine{}
	for _, it := range items {
		for _, p := range it.Images {
			b, err := fs.ReadFile(photos, p)
			if err != nil {
				exp.Fatal("%v", err)
			}
			pic := stimuli.NewPictureFromMemory(b, 0, 0)
			if err := stimuli.PreloadVisualOnScreen(exp.Screen, pic); err != nil {
				exp.Fatal("photo %s: %v", p, err)
			}
			pictures[p] = pic
		}
		w := stimuli.NewTextLine(it.Word, 0, 0, textColor)
		w.Font = font
		if err := stimuli.PreloadVisualOnScreen(exp.Screen, w); err != nil {
			exp.Fatal("word %q: %v", it.Word, err)
		}
		words[it.Index] = w
	}
	visual := func(t *Trial) stimuli.VisualStimulus {
		if t.Format == "image" {
			return pictures[t.Image]
		}
		return words[t.Item.Index]
	}
	frames := map[bool][]*stimuli.Rectangle{}
	for _, tg := range []bool{false, true} {
		c := frameColor
		if tg {
			c = targetColor
		}
		h := float32(frameSize) / 2
		frames[tg] = []*stimuli.Rectangle{
			stimuli.NewRectangle(0, h-frameWidth/2, frameSize, frameWidth, c),
			stimuli.NewRectangle(0, -h+frameWidth/2, frameSize, frameWidth, c),
			stimuli.NewRectangle(-h+frameWidth/2, 0, frameWidth, frameSize, c),
			stimuli.NewRectangle(h-frameWidth/2, 0, frameWidth, frameSize, c),
		}
	}

	// Data: one row per event, times in ms from the session start on the SDL clock.
	exp.AddDataVariableNames([]string{
		"run", "trial", "event", "format", "category", "item", "item_index", "word", "image",
		"target", "code", "time_ms", "run_time_ms", "intended_run_time_ms", "duration_ms",
		"response", "rt_ms", "outcome",
	})
	var t0 uint64
	ms := func(ns uint64) string {
		if ns == 0 {
			return "NA"
		}
		return strconv.FormatFloat(float64(int64(ns-t0))/1e6, 'f', 1, 64)
	}
	dur := func(a, b uint64) string {
		if a == 0 || b == 0 {
			return "NA"
		}
		return strconv.FormatFloat(float64(int64(b-a))/1e6, 'f', 1, 64)
	}
	row := func(run int, t *Trial, event string, code int, ts uint64, runTime, intended, duration, resp, rt, outcome string) {
		trial, format, cat, item, idx, word, img, tg := "NA", "NA", "NA", "NA", "NA", "NA", "NA", "NA"
		if t != nil {
			trial, format, cat, item = strconv.Itoa(t.Number), t.Format, t.Item.Category, t.Item.Name
			idx, word, tg = strconv.Itoa(t.Item.Index), t.Item.Word, map[bool]string{true: "1", false: "0"}[t.Target]
			if t.Image != "" {
				img = t.Image
			}
		}
		exp.Data.Add(run, trial, event, format, cat, item, idx, word, img, tg, code,
			ms(ts), runTime, intended, duration, resp, rt, outcome)
	}

	showMarked := func(v stimuli.VisualStimulus, code int) (uint64, error) {
		exp.Marker.Arm(byte(code))
		return exp.ShowTS(v)
	}
	waitSpace := func() (uint64, error) {
		if *auto {
			return control.TicksNS(), nil
		}
		exp.Keyboard.Clear()
		_, ts, err := exp.Keyboard.GetKeyEventTS([]control.Keycode{control.K_SPACE}, -1)
		return ts, err
	}

	// present shows one run (or the practice) as a single frame-locked stream,
	// and logs it. It returns the number of red frames detected.
	present := func(run int, seq []*Trial, marked bool) (int, error) {
		el := []stimuli.StreamElement{{Stimulus: fix, DurationOn: leadFixMs * time.Millisecond}}
		for _, t := range seq {
			el = append(el, stimuli.StreamElement{Stimulus: visual(t),
				DurationOn: stimOnMs * time.Millisecond, DurationOff: stimOffMs * time.Millisecond})
		}
		onFrame := func(ctx stimuli.FrameContext) error {
			if err := ctx.Screen.Clear(); err != nil {
				return err
			}
			if err := fix.Draw(ctx.Screen); err != nil {
				return err
			}
			if ctx.Index == 0 || !ctx.OnPhase {
				return nil
			}
			t := seq[ctx.Index-1]
			if marked && ctx.Frame == 0 {
				exp.Marker.Arm(t.Code())
			}
			if err := visual(t).Draw(ctx.Screen); err != nil {
				return err
			}
			for _, r := range frames[t.Target] {
				if err := r.Draw(ctx.Screen); err != nil {
					return err
				}
			}
			return nil
		}
		if buttons != nil {
			buttons.start()
		}
		runtime.GC()
		gc := debug.SetGCPercent(-1)
		events, timing, err := stimuli.PresentStreamOfStimuliHooks(exp.Screen, el, 0, 0, onFrame, nil)
		debug.SetGCPercent(gc)
		// One more flip so the photodiode square does not stay white.
		if _, ferr := exp.ShowTS(fix); err == nil {
			err = ferr
		}

		// Presses: keyboard events of the stream, plus the buttons.
		var presses []press
		for _, e := range events {
			if e.Event.Type == control.EVENT_KEY_DOWN {
				k := e.Event.KeyboardEvent().Key
				if k == control.K_SPACE || k == control.K_LEFT || k == control.K_RIGHT {
					presses = append(presses, press{e.TimestampNS, "key"})
				}
			}
		}
		if buttons != nil {
			presses = append(presses, buttons.finish()...)
		}
		if *auto {
			presses = autoPresses(seq, timing)
		}

		// Score: a press within respWindowMs of a red frame's onset is a hit
		// (first one only); any other press is a false alarm.
		runStart := uint64(0)
		if len(timing) > 0 {
			runStart = timing[0].OnsetNS
		}
		used := make([]bool, len(presses))
		hits := 0
		outcome := make([]string, len(seq))
		rts := make([]string, len(seq))
		for i, t := range seq {
			outcome[i], rts[i] = "NA", "NA"
			if !t.Target {
				continue
			}
			outcome[i] = "miss"
			if i+1 >= len(timing) {
				continue
			}
			on := timing[i+1].OnsetNS
			for k, p := range presses {
				if !used[k] && p.ns >= on && p.ns <= on+respWindowMs*1_000_000 {
					used[k] = true
					outcome[i], rts[i] = "hit", dur(on, p.ns)
					hits++
					break
				}
			}
		}
		if marked || run == 0 {
			if len(timing) > 0 {
				row(run, nil, "fixation", 0, timing[0].OnsetNS, "0.0", "0", dur(timing[0].OnsetNS, timing[0].OffsetNS), "NA", "NA", "NA")
			}
			for i, t := range seq {
				if i+1 >= len(timing) {
					break
				}
				tl := timing[i+1]
				intended := strconv.Itoa(leadFixMs + i*(stimOnMs+stimOffMs))
				code := 0
				if marked {
					code = int(t.Code())
				}
				event := "stimulus"
				if run == 0 {
					event = "practice"
				}
				row(run, t, event, code, tl.OnsetNS, dur(runStart, tl.OnsetNS), intended, dur(tl.OnsetNS, tl.OffsetNS), "NA", rts[i], outcome[i])
			}
			fa := 0
			for k, p := range presses {
				if !used[k] {
					fa++
					row(run, nil, "false_alarm", 0, p.ns, dur(runStart, p.ns), "NA", "NA", p.source, "NA", "false_alarm")
				} else {
					row(run, nil, "press", 0, p.ns, dur(runStart, p.ns), "NA", "NA", p.source, "NA", "hit")
				}
			}
			n := 0
			for _, t := range seq {
				if t.Target {
					n++
				}
			}
			summary := fmt.Sprintf("run %d: %d/%d red frames detected, %d false alarms", run, hits, n, fa)
			if run == 0 {
				summary = fmt.Sprintf("practice: %d/%d red frames detected, %d false alarms", hits, n, fa)
			}
			log.Print(summary)
			exp.AddExperimentInfo(summary)
			_ = exp.Data.Save()
		}
		return hits, err
	}

	err = exp.Run(func() error {
		t0 = control.TicksNS()
		for run := *startRun; run <= nRuns; run++ {
			text := pauseText(run)
			if run == *startRun {
				text = instructionText(*meg)
			}
			box := exp.FittedTextBox(text)
			onset, err := showMarked(box, codeRunStart+run)
			box.Unload()
			if err != nil {
				return err
			}
			row(run, nil, "pause", codeRunStart+run, onset, "NA", "NA", "NA", "NA", "NA", "NA")
			ts, err := waitSpace()
			if err != nil {
				return err
			}
			row(run, nil, "pause_end", 0, ts, "NA", "NA", dur(onset, ts), "space", "NA", "NA")

			if run == 1 && len(practiceTrials) > 0 {
				hits, err := present(0, practiceTrials, false)
				if err != nil {
					return err
				}
				box := exp.FittedTextBox(practiceDoneText(hits, 3))
				if _, err := exp.ShowTS(box); err != nil {
					return err
				}
				box.Unload()
				if _, err := waitSpace(); err != nil {
					return err
				}
			}
			if _, err := present(run, runs[run], true); err != nil {
				return err
			}
		}
		box := exp.FittedTextBox(endText)
		onset, err := showMarked(box, codeEnd)
		if err != nil {
			return err
		}
		row(0, nil, "end", codeEnd, onset, "NA", "NA", "NA", "NA", "NA", "NA")
		if err := exp.Wait(endScreenMs); err != nil {
			return err
		}
		return control.EndLoop
	})
	if err != nil && !control.IsEndLoop(err) {
		exp.Fatal("experiment error: %v", err)
	}
}

// autoPresses simulates a participant for -auto: a press 400-700 ms after
// each red frame, misses one in ten.
func autoPresses(seq []*Trial, timing []stimuli.TimingLog) []press {
	var out []press
	k := 0
	for i, t := range seq {
		if !t.Target || i+1 >= len(timing) {
			continue
		}
		k++
		if k%10 == 0 {
			continue
		}
		out = append(out, press{timing[i+1].OnsetNS + uint64(400+(k*37)%300)*1_000_000, "auto"})
	}
	return out
}

// newDot returns a filled, antialiased circle (as in MEG-pronouns-main).
func newDot(radius float32, c control.Color) *stimuli.Picture {
	const ss = 8
	size := int(math.Ceil(float64(2*radius))) + 2
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	center := float64(size) / 2
	r2 := float64(radius) * float64(radius)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			in := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					dx := float64(x) + (float64(sx)+0.5)/ss - center
					dy := float64(y) + (float64(sy)+0.5)/ss - center
					if dx*dx+dy*dy <= r2 {
						in++
					}
				}
			}
			img.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, uint8(255 * in / (ss * ss))})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return stimuli.NewPictureFromMemory(buf.Bytes(), 0, 0)
}

// projectDir is the folder holding the executable (or the current folder
// under go run).
func projectDir() string {
	exe, err := os.Executable()
	if err != nil || strings.HasPrefix(exe, os.TempDir()) || strings.Contains(exe, "go-build") {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

func flagGiven(name string) bool {
	given := false
	flag.Visit(func(f *flag.Flag) { given = given || f.Name == name })
	return given
}

// moveDataFiles moves the session's data and info files to dir (as in
// MEG-pronouns-main).
func moveDataFiles(d *results.DataFile, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range []*results.OutputFile{d.OutputFile, d.InfoFile} {
		if err := f.Save(); err != nil {
			return err
		}
		dest := filepath.Join(dir, filepath.Base(f.FullPath))
		if err := os.Rename(f.FullPath, dest); err != nil {
			b, rerr := os.ReadFile(f.FullPath)
			if rerr != nil {
				return rerr
			}
			if werr := os.WriteFile(dest, b, 0o644); werr != nil {
				return werr
			}
			if rmErr := os.Remove(f.FullPath); rmErr != nil {
				return rmErr
			}
		}
		if old := f.Directory; old != dir {
			_ = os.Remove(old)
		}
		f.Directory, f.FullPath = dir, dest
	}
	return nil
}
