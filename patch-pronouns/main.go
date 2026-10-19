// Pronouns — MEG experiment on pronoun resolution (French).
//
// Each trial: fixation (1000 ms), sentence 1 word by word (6 words, each
// 200 ms on + 300 ms fixation), a 1000 ms break, sentence 2 (3 words, same
// timing), then a felicity judgment (two blue buttons, max 3000 ms) answered
// once the last word has gone off, and 500 ms of feedback on the fixation
// dot (green correct, red incorrect, dark grey timeout). 160 trials per subject
// in 5 blocks of 32, with a pause before each block that the operator ends with SPACE. Trials come
// from counterbalancing/counterbalancing.csv (make_counterbalancing.py), embedded in the binary.
//
// Trigger codes (see triggers/make_trigger_table.py, triggers/triggers.csv):
//
//	16*role + cell  word onsets (role 1-9, cell = condition bits, 16-159)
//	160 + trial     trial start, fixation onset (161-192)
//	200 + block     pause / instruction screen before block (201-205)
//	211 / 212 / 213 feedback onset: correct / incorrect / timeout
//	255             end screen
//
// Every code is written right after the flip that shows the event, together
// with the photodiode square (top-left). Button presses are recorded by the
// MEG itself on its own STI lines.
//
// Usage:
//
//	go run . -w -s 1               # windowed test, keyboard (← / →)
//	go run . -s 1 -meg             # MEG: parallel-port triggers and buttons
//	go run . -s 1 -meg -block 3    # resume a session at block 3
package main

import (
	"bytes"
	_ "embed"
	"encoding/csv"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/chrplr/goxpyriment/apparatus"
	"github.com/chrplr/goxpyriment/assets_embed"
	"github.com/chrplr/goxpyriment/control"
	"github.com/chrplr/goxpyriment/results"
	"github.com/chrplr/goxpyriment/stimuli"
	"github.com/chrplr/goxpyriment/triggers"
)

//go:embed counterbalancing/counterbalancing.csv
var counterbalancingCSV []byte

// ---------------------------------------------------------------------------
// Timing (ms)

const (
	fixationMs       = 1000 // initial fixation, and the break between sentences
	wordOnMs         = 200
	wordOffMs        = 300
	responseWindowMs = 3000
	feedbackMs       = 500
	endScreenMs      = 5000
	nBlocks          = 5
)

// dataDir is where the data files go, relative to the project folder (the
// folder holding the executable), so the folder can be copied anywhere.
const dataDir = "data"

// ---------------------------------------------------------------------------
// Appearance (change freely)

var (
	backgroundColor = control.Black
	textColor       = control.White
	fixColor        = control.RGB(192, 192, 192) // light grey
	correctColor    = control.RGB(153, 229, 153)
	incorrectColor  = control.RGB(240, 128, 128)
	timeoutColor    = control.RGB(96, 96, 96) // dark grey
)

const (
	defaultFontSize = 48 // point size of the RSVP words
	fixRadius       = 4  // fixation dot, in pixels
)

// ---------------------------------------------------------------------------
// MEG hardware (NeuroSpin, as in the Pushmi experiment)
//
// Triggers go out on the data lines of /dev/parport1. The response pad's BLUE
// buttons come in on status lines (rig table, 19/10/2026, the red ones being
// unreliable): left blue = port 2 value 0b1000, right blue = port 0 value
// 0b1000, in expyriment's read_status convention (status register >> 3), i.e.
// status bit 6 (0x40, pin 10 nAck) of /dev/parport2 and of /dev/parport0.
// Same table for the red buttons: port 0 0b0100 and port 1 0b0010, i.e. the
// previous setting 0x20 on /dev/parport0 and 0x10 on /dev/parport1.
// A button is down when its bit differs from the value read at startup.

const (
	triggerPort     = "/dev/parport1"
	leftButtonPort  = "/dev/parport2"
	rightButtonPort = "/dev/parport0"
	leftButtonMask  = 0x40 // status bit 6 = expyriment read_status 0b1000
	rightButtonMask = 0x40 // status bit 6 = expyriment read_status 0b1000
	pollInterval    = 500 * time.Microsecond
)

// ---------------------------------------------------------------------------
// Trigger codes

const (
	codeTrialStart = 160 // + trial_number
	codeBlockStart = 200 // + block_number
	codeCorrect    = 211
	codeIncorrect  = 212
	codeTimeout    = 213
	codeEnd        = 255
)

// Word roles, numbered as in the trigger table.
const (
	roleDetSubj = iota + 1
	roleNounSubj
	roleVerb
	roleDans
	roleDetPlace
	roleNounPlace
	rolePronoun
	roleEtait
	roleAdjective
)

var roleNames = map[int]string{
	roleDetSubj: "det_subj", roleNounSubj: "noun_subj", roleVerb: "verb",
	roleDans: "dans", roleDetPlace: "det_place", roleNounPlace: "noun_place",
	rolePronoun: "pronoun", roleEtait: "etait", roleAdjective: "adjective",
}

var (
	canonicalRoles    = []int{roleDetSubj, roleNounSubj, roleVerb, roleDans, roleDetPlace, roleNounPlace}
	nonCanonicalRoles = []int{roleDans, roleDetPlace, roleNounPlace, roleDetSubj, roleNounSubj, roleVerb}
	sentence2Roles    = []int{rolePronoun, roleEtait, roleAdjective}
)

// ---------------------------------------------------------------------------
// Trials

type word struct {
	text     string
	role     int
	sentence int // 1 or 2
	position int // 1-based, within its sentence
	code     byte
	stim     *stimuli.TextLine
}

type trial struct {
	block, number int
	fields        map[string]string // the counterbalancing row
	cell          int
	correctKey    string // "left" or "right"
	words         []word // the 6 words of sentence 1, then the 3 of sentence 2
}

// loadTrials returns the subject's trials from the counterbalancing table, in
// presentation order, with each word's role and trigger code.
func loadTrials(raw []byte, subject int) ([]*trial, error) {
	r := csv.NewReader(bytes.NewReader(raw))
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	var trials []*trial
	for line := 2; ; line++ {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		f := make(map[string]string, len(header))
		for i, h := range header {
			f[h] = rec[i]
		}
		if f["subject_id"] != strconv.Itoa(subject) {
			continue
		}
		t, err := newTrial(f)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		trials = append(trials, t)
	}
	if len(trials) == 0 {
		return nil, fmt.Errorf("no trials for subject %d", subject)
	}
	for i, t := range trials {
		if want := (i/32+1)*1000 + i%32 + 1; t.block*1000+t.number != want {
			return nil, fmt.Errorf("trial %d is block %d trial %d: rows not in presentation order", i+1, t.block, t.number)
		}
	}
	return trials, nil
}

func newTrial(f map[string]string) (*trial, error) {
	t := &trial{fields: f, correctKey: f["correct_key"]}
	var err error
	if t.block, err = strconv.Atoi(f["block_number"]); err != nil {
		return nil, fmt.Errorf("block_number: %w", err)
	}
	if t.number, err = strconv.Atoi(f["trial_number"]); err != nil {
		return nil, fmt.Errorf("trial_number: %w", err)
	}
	if t.correctKey != "left" && t.correctKey != "right" {
		return nil, fmt.Errorf("correct_key %q", t.correctKey)
	}

	canonical := f["canonical_order"] == "1"
	if f["congruent"] == "1" {
		t.cell |= 1
	}
	if canonical {
		t.cell |= 2
	}
	if f["pronoun_gender"] == "f" {
		t.cell |= 4
	}
	if f["agent_gender"] == "f" {
		t.cell |= 8
	}

	roles1 := nonCanonicalRoles
	if canonical {
		roles1 = canonicalRoles
	}
	for s, spec := range []struct {
		text  string
		roles []int
	}{{f["sentence1"], roles1}, {f["sentence2"], sentence2Roles}} {
		texts := strings.Fields(strings.TrimSuffix(spec.text, "."))
		if len(texts) != len(spec.roles) {
			return nil, fmt.Errorf("sentence%d %q has %d words, expected %d", s+1, spec.text, len(texts), len(spec.roles))
		}
		for i, txt := range texts {
			role := spec.roles[i]
			t.words = append(t.words, word{
				text: txt, role: role, sentence: s + 1, position: i + 1,
				code: byte(16*role + t.cell),
			})
		}
	}

	// The roles must match the words the table says the sentence contains.
	check := map[int]string{
		roleNounSubj: f["subject"], roleVerb: f["verb"], roleAdjective: f["adjective"],
	}
	for _, w := range t.words {
		if want, ok := check[w.role]; ok && w.text != want {
			return nil, fmt.Errorf("%s is %q, expected %q (%s)", roleNames[w.role], w.text, want, f["stimulus"])
		}
	}
	return t, nil
}

// ---------------------------------------------------------------------------
// Responses

// responder reports the first press of the left or right response button.
type responder interface {
	// poll returns "left", "right" or "" and the SDL-clock time of the check.
	// It returns control.EndLoop on ESC or window close.
	poll() (string, uint64, error)
	desc() string
}

// keyboardResponder: ← and → (offline / behavioural testing).
type keyboardResponder struct{ kb *apparatus.Keyboard }

func (k keyboardResponder) poll() (string, uint64, error) {
	key, ts, quit := k.kb.PollKeysWithTS()
	switch {
	case quit || key == control.K_ESCAPE:
		return "", 0, control.EndLoop
	case key == control.K_LEFT:
		return "left", ts, nil
	case key == control.K_RIGHT:
		return "right", ts, nil
	}
	return "", 0, nil
}

func (keyboardResponder) desc() string { return "keyboard (left/right arrows)" }

// autoResponder answers at random 500-1500 ms after it starts being polled,
// for unattended timing checks (-auto).
type autoResponder struct {
	kb      *apparatus.Keyboard
	rng     *rand.Rand
	startNS uint64
}

func (a *autoResponder) poll() (string, uint64, error) {
	if key, _, quit := a.kb.PollKeysWithTS(); quit || key == control.K_ESCAPE {
		return "", 0, control.EndLoop
	}
	now := control.TicksNS()
	if a.startNS == 0 {
		a.startNS = now
		return "", 0, nil
	}
	if now-a.startNS < uint64(500+a.rng.IntN(1000))*1_000_000 {
		return "", 0, nil
	}
	a.startNS = 0
	return [2]string{"left", "right"}[a.rng.IntN(2)], now, nil
}

func (*autoResponder) desc() string { return "automatic random answers (-auto)" }

// buttonResponder reads the MEG response pad through the parallel ports'
// status lines. The operator's keyboard still answers ESC.
type buttonResponder struct {
	kb          *apparatus.Keyboard
	left, right *triggers.ParallelPort
	baseL       byte
	baseR       byte
	downL       bool
	downR       bool
}

func newButtonResponder(kb *apparatus.Keyboard, left, right *triggers.ParallelPort) (*buttonResponder, error) {
	b := &buttonResponder{kb: kb, left: left, right: right}
	var err error
	if b.baseL, err = left.ReadStatus(); err != nil {
		return nil, err
	}
	if b.baseR, err = right.ReadStatus(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *buttonResponder) poll() (string, uint64, error) {
	if key, _, quit := b.kb.PollKeysWithTS(); quit || key == control.K_ESCAPE {
		return "", 0, control.EndLoop
	}
	ts := control.TicksNS()
	sl, err := b.left.ReadStatus()
	if err != nil {
		return "", 0, err
	}
	sr, err := b.right.ReadStatus()
	if err != nil {
		return "", 0, err
	}
	// Report a button on the transition to "down" only, so a button held
	// from the previous screen is not taken as a new answer.
	downL := (sl^b.baseL)&leftButtonMask != 0
	downR := (sr^b.baseR)&rightButtonMask != 0
	pressL, pressR := downL && !b.downL, downR && !b.downR
	b.downL, b.downR = downL, downR
	switch {
	case pressL:
		return "left", ts, nil
	case pressR:
		return "right", ts, nil
	}
	return "", 0, nil
}

func (b *buttonResponder) desc() string {
	return fmt.Sprintf("MEG blue buttons: left %s mask 0x%02x, right %s mask 0x%02x (baselines 0x%02x, 0x%02x)",
		leftButtonPort, leftButtonMask, rightButtonPort, rightButtonMask, b.baseL, b.baseR)
}

// drain forgets presses made before now (keyboard queue, buttons held down).
func drain(r responder) error {
	for i := 0; i < 1000; i++ {
		btn, _, err := r.poll()
		if err != nil {
			return err
		}
		if btn == "" {
			return nil
		}
	}
	return nil
}

// waitResponse polls until a button is pressed or deadlineNS passes. It
// returns "" and 0 on timeout.
func waitResponse(r responder, deadlineNS uint64) (string, uint64, error) {
	for {
		btn, ts, err := r.poll()
		if err != nil || btn != "" {
			return btn, ts, err
		}
		if deadlineNS > 0 && control.TicksNS() >= deadlineNS {
			return "", 0, nil
		}
		time.Sleep(pollInterval)
	}
}

// ---------------------------------------------------------------------------
// Instructions (French placeholders)

// mappingText names the two buttons for this subject's response mapping.
func mappingText(congruentKey string) string {
	other := map[string]string{"left": "DROIT", "right": "GAUCHE"}[congruentKey]
	this := map[string]string{"left": "GAUCHE", "right": "DROIT"}[congruentKey]
	return "bouton bleu " + this + " : oui, la suite est naturelle\n" +
		"bouton bleu " + other + " : non, la suite est étrange"
}

func instructionText(congruentKey string) string {
	return "Bienvenue !\n\n" +
		"Vous allez lire des paires de phrases, présentées mot par mot au centre de l'écran.\n\n" +
		"Après la seconde phrase, indiquez si elle s'enchaîne naturellement avec la première :\n\n" +
		mappingText(congruentKey) + "\n\n" +
		"Répondez dès que le dernier mot a disparu. Le point de fixation devient " +
		"verte si votre réponse est correcte, rouge si elle est incorrecte, et gris foncé " +
		"si vous n'avez pas répondu à temps.\n\n" +
		"Gardez les yeux sur le point et évitez de cligner des yeux pendant la lecture."
}

func pauseText(done int, congruentKey string) string {
	return fmt.Sprintf("Pause\n\nBloc %d sur %d terminé.\n\n"+
		"Reposez-vous quelques instants.\n\n"+
		"Rappel :\n%s", done, nBlocks, mappingText(congruentKey))
}

const endText = "L'expérience est terminée.\n\nMerci pour votre participation !"

// ---------------------------------------------------------------------------

func main() {
	meg := flag.Bool("meg", false, "MEG mode: triggers on "+triggerPort+", answers with the blue buttons of the response pad (otherwise: keyboard arrows, no triggers)")
	photodiode := flag.String("photodiode", "auto", "photodiode square in the top-left corner: on, off, or auto (on with -meg)")
	photodiodeSize := flag.Float64("photodiode-size", control.DefaultPhotodiodeSize, "side of the photodiode square, in pixels")
	ttlMs := flag.Float64("ttl-ms", 0, "how long each trigger code stays on the lines, in ms (0 = 3 frames, 25 ms at 120 Hz)")
	startBlock := flag.Int("block", 1, "block to start from (to resume an interrupted session)")
	fontPath := flag.String("font", "", "TTF font file for the words (default: the bundled Inconsolata)")
	fontSize := flag.Float64("font-size", defaultFontSize, "point size of the words")
	auto := flag.Bool("auto", false, "answer automatically at random and start blocks without SPACE (timing checks)")
	outDir := flag.String("o", dataDir, "folder for the data files; a relative path is taken from the project folder")
	cbPath := flag.String("cb", "", "read the counterbalancing table from this file instead of the embedded copy")

	// The setup dialog (shown when -s is not given) offers the same default.
	exp := control.NewExperimentFromFlags("Pronouns", backgroundColor, textColor, 32,
		control.InfoField{Name: "output_dir", Label: "Results folder (relative to the Pronouns folder)", Default: dataDir})
	defer exp.End()

	// -o if given, else the dialog's folder, else the default; a relative
	// folder is taken from the project folder.
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

	if *startBlock < 1 || *startBlock > nBlocks {
		exp.Fatal("-block %d: must be between 1 and %d", *startBlock, nBlocks)
	}

	// Trials.
	cb, cbSource := counterbalancingCSV, "embedded:counterbalancing/counterbalancing.csv"
	if *cbPath != "" {
		var err error
		if cb, err = os.ReadFile(*cbPath); err != nil {
			exp.Fatal("%v", err)
		}
		cbSource = *cbPath
	}
	trials, err := loadTrials(cb, exp.SubjectID)
	if err != nil {
		exp.Fatal("%s: %v", cbSource, err)
	}
	congruentKey := trials[0].fields["congruent_key"]
	log.Printf("subject %d: %d trials from %s, congruent = %s button", exp.SubjectID, len(trials), cbSource, congruentKey)

	// Hardware: one parallel port carries both the trigger codes (data lines)
	// and the right button (status lines), so it is opened once, here, and
	// shared by the onset marker and the button reader.
	var resp responder = keyboardResponder{exp.Keyboard}
	var ttlOut apparatus.MarkerOutput
	if *meg {
		trig := triggers.NewParallelPort(triggerPort)
		if err := trig.Open(); err != nil {
			exp.Fatal("trigger port: %v", err)
		}
		ttlOut = trig
		// Each port is opened once, even when it carries a button and the
		// triggers, or both buttons.
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
		left, right := openPort(leftButtonPort), openPort(rightButtonPort)
		for path, p := range ports {
			if path != triggerPort {
				defer p.Close()
			}
		}
		b, err := newButtonResponder(exp.Keyboard, left, right)
		if err != nil {
			exp.Fatal("response buttons: %v", err)
		}
		resp = b
	}
	if *auto { // after -meg: the triggers still go out
		resp = &autoResponder{kb: exp.Keyboard, rng: rand.New(rand.NewPCG(1, 2))}
	}
	usePhotodiode := *photodiode == "on" || (*photodiode == "auto" && *meg)
	if *photodiode != "on" && *photodiode != "off" && *photodiode != "auto" {
		exp.Fatal("-photodiode %q: on, off or auto", *photodiode)
	}
	if usePhotodiode || ttlOut != nil {
		desc := ""
		if ttlOut != nil {
			desc = "parallel port " + triggerPort
		}
		exp.Marker = apparatus.NewMarker(apparatus.MarkerOptions{
			Photodiode:  usePhotodiode,
			Size:        float32(*photodiodeSize),
			Output:      ttlOut,
			OutputDesc:  desc,
			TTLDuration: time.Duration(*ttlMs * float64(time.Millisecond)),
		})
		exp.Screen.SetMarker(exp.Marker)
		if ttlOut != nil {
			if err := ttlOut.AllLow(); err != nil {
				exp.Fatal("trigger port: %v", err)
			}
		}
	}
	exp.AddExperimentInfo("counterbalancing: " + cbSource)
	exp.AddExperimentInfo("responses: " + resp.desc())
	exp.AddExperimentInfo(fmt.Sprintf("triggers: %v; photodiode: %v (%.0f px, top-left); ttl duration: %v",
		ttlOut != nil, usePhotodiode, *photodiodeSize, exp.Marker.TTLDuration()))
	exp.AddExperimentInfo(fmt.Sprintf("congruent_key: %s; start block: %d", congruentKey, *startBlock))
	log.Print("responses: " + resp.desc())

	// Stimuli, all built and uploaded before the run.
	font, err := control.FontFromMemory(assets_embed.InconsolataFont, float32(*fontSize))
	if *fontPath != "" {
		font, err = control.FontFromFile(*fontPath, float32(*fontSize))
	}
	if err != nil {
		exp.Fatal("font: %v", err)
	}
	fix := newDot(fixRadius, fixColor)
	fixCorrect := newDot(fixRadius, correctColor)
	fixIncorrect := newDot(fixRadius, incorrectColor)
	fixTimeout := newDot(fixRadius, timeoutColor)
	for _, v := range []stimuli.VisualStimulus{fix, fixCorrect, fixIncorrect, fixTimeout} {
		if err := stimuli.PreloadVisualOnScreen(exp.Screen, v); err != nil {
			exp.Fatal("preloading the fixation dot: %v", err)
		}
	}
	for _, t := range trials {
		for i := range t.words {
			w := &t.words[i]
			w.stim = stimuli.NewTextLine(w.text, 0, 0, textColor)
			w.stim.Font = font
			if err := stimuli.PreloadVisualOnScreen(exp.Screen, w.stim); err != nil {
				exp.Fatal("preloading %q: %v", w.text, err)
			}
		}
	}

	// Data: one row per event. Times are ms from the session start (the
	// start of the run) on the SDL clock that also stamps flips and key
	// presses; for words, the intended time is relative to the trial onset.
	exp.AddDataVariableNames([]string{
		"block_number", "trial_number", "event", "sentence", "position", "role", "word", "code",
		"time_ms", "trial_time_ms", "intended_trial_time_ms", "duration_ms",
		"response", "correct_key", "correct", "rt_ms",
		"cell", "congruent", "canonical_order", "pronoun_gender", "agent_gender", "pronoun_referent", "stimulus",
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
	addRow := func(t *trial, event string, w *word, code int, ts uint64, trialTime, intended, duration string,
		response, correct, rt string) {
		block, number, sentence, position, role, text := "NA", "NA", "NA", "NA", "NA", "NA"
		cell, cong, canon, pg, ag, ref, stim, ck := "NA", "NA", "NA", "NA", "NA", "NA", "NA", "NA"
		if t != nil {
			block, number = strconv.Itoa(t.block), strconv.Itoa(t.number)
			cell, ck = strconv.Itoa(t.cell), t.correctKey
			cong, canon = t.fields["congruent"], t.fields["canonical_order"]
			pg, ag, ref = t.fields["pronoun_gender"], t.fields["agent_gender"], t.fields["pronoun_referent"]
			stim = t.fields["stimulus"]
		}
		if w != nil {
			sentence, position = strconv.Itoa(w.sentence), strconv.Itoa(w.position)
			role, text = roleNames[w.role], w.text
		}
		exp.Data.Add(block, number, event, sentence, position, role, text, code,
			ms(ts), trialTime, intended, duration, response, ck, correct, rt,
			cell, cong, canon, pg, ag, ref, stim)
	}

	// showMarked presents v with code on the trigger lines and the photodiode
	// square, and returns the flip time.
	showMarked := func(v stimuli.VisualStimulus, code int) (uint64, error) {
		exp.Marker.Arm(byte(code))
		return exp.ShowTS(v)
	}

	// The fixed part of every trial's stream: fixation, 6 words, break, 3 words.
	// The last word of sentence 1 is followed by its 300 ms of fixation plus
	// the 1000 ms break; the adjective's stream ends when it goes off.
	buildStream := func(t *trial) []stimuli.StreamElement {
		el := []stimuli.StreamElement{{Stimulus: fix, DurationOn: fixationMs * time.Millisecond}}
		for i, w := range t.words {
			off := wordOffMs * time.Millisecond
			switch {
			case i == 5:
				off += fixationMs * time.Millisecond
			case i == len(t.words)-1:
				off = 0
			}
			el = append(el, stimuli.StreamElement{Stimulus: w.stim, DurationOn: wordOnMs * time.Millisecond, DurationOff: off})
		}
		return el
	}

	err = exp.Run(func() error {
		t0 = control.TicksNS()

		for _, t := range trials {
			if t.block < *startBlock {
				continue
			}

			// Pause (or instructions) before each block.
			if t.number == 1 {
				runtime.GC()
				text := pauseText(t.block-1, congruentKey)
				if t.block == *startBlock {
					text = instructionText(congruentKey)
				}
				box := exp.FittedTextBox(text)
				onset, err := showMarked(box, codeBlockStart+t.block)
				box.Unload()
				if err != nil {
					return err
				}
				addRow(nil, "pause", nil, codeBlockStart+t.block, onset, "NA", "NA", "NA", "NA", "NA", "NA")
				// The operator starts the block with SPACE.
				ts := control.TicksNS()
				if !*auto {
					exp.Keyboard.Clear()
					if _, ts, err = exp.Keyboard.GetKeyEventTS([]control.Keycode{control.K_SPACE}, -1); err != nil {
						return err
					}
				}
				addRow(nil, "pause_end", nil, 0, ts, "NA", "NA", dur(onset, ts), "space", "NA", "NA")
			}

			// Words.
			trialCode := codeTrialStart + t.number
			onFrame := func(ctx stimuli.FrameContext) error {
				if ctx.OnPhase && ctx.Frame == 0 {
					code := trialCode
					if ctx.Index > 0 {
						code = int(t.words[ctx.Index-1].code)
					}
					exp.Marker.Arm(byte(code))
				}
				// The dot stays on screen throughout, under the words.
				if err := ctx.Screen.Clear(); err != nil {
					return err
				}
				if err := fix.Draw(ctx.Screen); err != nil {
					return err
				}
				if ctx.OnPhase && ctx.Index > 0 {
					return t.words[ctx.Index-1].stim.Draw(ctx.Screen)
				}
				return nil
			}
			gc := debug.SetGCPercent(-1)
			_, timing, err := stimuli.PresentStreamOfStimuliHooks(exp.Screen, buildStream(t), 0, 0, onFrame, nil)
			if err != nil {
				debug.SetGCPercent(gc)
				return err
			}

			// Response window: from the flip that takes the adjective down.
			openNS, err := exp.ShowTS(fix)
			if err != nil {
				debug.SetGCPercent(gc)
				return err
			}
			if err := drain(resp); err != nil {
				debug.SetGCPercent(gc)
				return err
			}
			btn, pressNS, err := waitResponse(resp, openNS+responseWindowMs*1_000_000)
			if err != nil {
				debug.SetGCPercent(gc)
				return err
			}

			// Feedback.
			fb, code, correct := fixTimeout, codeTimeout, "NA"
			switch {
			case btn == t.correctKey:
				fb, code, correct = fixCorrect, codeCorrect, "1"
			case btn != "":
				fb, code, correct = fixIncorrect, codeIncorrect, "0"
			}
			fbNS, err := showMarked(fb, code)
			debug.SetGCPercent(gc)
			if err != nil {
				return err
			}

			// Log the trial.
			trialNS := timing[0].OnsetNS
			rel := func(ns uint64) string { return dur(trialNS, ns) }
			addRow(t, "fixation", nil, trialCode, trialNS, "0.0", "0", dur(timing[0].OnsetNS, timing[0].OffsetNS), "NA", "NA", "NA")
			for i := range t.words {
				w := &t.words[i]
				tl := timing[i+1]
				intended := fixationMs + (w.position-1)*(wordOnMs+wordOffMs)
				if w.sentence == 2 {
					intended += 6*(wordOnMs+wordOffMs) + fixationMs
				}
				// The on-phase ends at the next flip: the next element's onset,
				// or (for the adjective) the response-window flip.
				offNS := openNS
				if i+2 < len(timing) {
					offNS = timing[i+1].OffsetNS
				}
				addRow(t, "word", w, int(w.code), tl.OnsetNS, rel(tl.OnsetNS), strconv.Itoa(intended), dur(tl.OnsetNS, offNS), "NA", "NA", "NA")
			}
			respIntended := strconv.Itoa(2*fixationMs + 8*(wordOnMs+wordOffMs) + wordOnMs)
			addRow(t, "response_window", nil, 0, openNS, rel(openNS), respIntended, "NA", "NA", "NA", "NA")
			rt := "NA"
			if btn != "" {
				rt = dur(openNS, pressNS)
				addRow(t, "response", nil, 0, pressNS, rel(pressNS), "NA", "NA", btn, correct, rt)
			}
			event := map[int]string{codeCorrect: "feedback_correct", codeIncorrect: "feedback_incorrect", codeTimeout: "feedback_timeout"}[code]
			addRow(t, event, nil, code, fbNS, rel(fbNS), "NA", "NA", btn, correct, rt)

			if err := exp.Wait(feedbackMs - int((control.TicksNS()-fbNS)/1_000_000)); err != nil {
				return err
			}
		}

		box := exp.FittedTextBox(endText)
		onset, err := showMarked(box, codeEnd)
		if err != nil {
			return err
		}
		addRow(nil, "end", nil, codeEnd, onset, "NA", "NA", "NA", "NA", "NA", "NA")
		if err := exp.Wait(endScreenMs); err != nil {
			return err
		}
		return control.EndLoop
	})
	if err != nil && !control.IsEndLoop(err) {
		exp.Fatal("experiment error: %v", err)
	}
}

// newDot returns a filled circle of the given radius as an antialiased image
// (the library's Circle is drawn in pixel rows and looks jagged when small).
func newDot(radius float32, c control.Color) *stimuli.Picture {
	const ss = 8 // supersampling per axis
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
		panic(err) // encoding an in-memory image cannot fail
	}
	return stimuli.NewPictureFromMemory(buf.Bytes(), 0, 0)
}

// projectDir is the Pronouns folder: the folder holding the executable, or
// the current folder when running a temporary executable built by go run.
func projectDir() string {
	exe, err := os.Executable()
	if err != nil || strings.HasPrefix(exe, os.TempDir()) || strings.Contains(exe, "go-build") {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

// flagGiven reports whether a flag was set on the command line.
func flagGiven(name string) bool {
	given := false
	flag.Visit(func(f *flag.Flag) { given = given || f.Name == name })
	return given
}

// moveDataFiles moves the session's data and info files to dir. The library
// creates them before the program can resolve the folder: in ~/goxpy_data, or
// in the dialog's folder taken relative to the current directory.
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
			// Different file system: copy, then remove the original.
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
			_ = os.Remove(old) // only succeeds if the move left it empty
		}
		f.Directory, f.FullPath = dir, dest
	}
	return nil
}
