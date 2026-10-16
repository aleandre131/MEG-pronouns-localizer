package main

// sequence.go: the items, the trigger codes and the order of the trials.
// Everything here is independent of the screen and of the MEG hardware, and is
// covered by main_test.go.

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Design (per subject: 2 runs, done in one session with a pause between).
const (
	nRuns          = 2
	repsPerRun     = 6  // presentations of each item in each format, per run
	targetsPerRun  = 20 // red frames per run: 10 images + 10 words, half persons half places
	targetMinStart = 10 // no target among the first trials of a run
	targetMinGap   = 8  // trials between two targets, at least
	maxSameCat     = 4  // at most this many persons (or places) in a row
)

// Trigger codes (value read on STI101, or STI001-STI008):
//
//	1-32    image onset, white frame   (code = item index)
//	33-64   word onset, white frame    (code = 32 + item index)
//	65-96   image onset, RED frame     (code = 64 + item index)
//	97-128  word onset, RED frame      (code = 96 + item index)
//	200+run instruction / pause screen before the run (201, 202)
//	255     end screen
//
// Item index: 1-16 persons, 17-32 places (items.csv). The response buttons are
// recorded by the MEG on their own STI lines.
const (
	codeWordOffset   = 32
	codeTargetOffset = 64
	codeRunStart     = 200
	codeEnd          = 255
)

// Item is one row of items.csv.
type Item struct {
	Index    int    // 1-32
	Category string // "person" or "place"
	Name     string // folder name of its photos (assets/images/<persons|places>/<Name>)
	Word     string // the word shown on word trials (the noun, no article)
	Gender   string
	Images   []string // paths of its normalised photos inside the embedded FS
}

func (it *Item) folder() string {
	if it.Category == "person" {
		return "persons"
	}
	return "places"
}

// Trial is one presentation (500 ms) followed by its fixation (500 ms).
type Trial struct {
	Run, Number int    // Number: 1-based within the run
	Format      string // "image" or "word"
	Item        *Item
	Image       string // photo path, image trials only
	Target      bool   // red frame
}

// Code is the trigger written at the trial's onset.
func (t *Trial) Code() byte {
	c := t.Item.Index
	if t.Format == "word" {
		c += codeWordOffset
	}
	if t.Target {
		c += codeTargetOffset
	}
	return byte(c)
}

// loadItems reads items.csv and attaches each item's photos from images (an
// fs.FS rooted so that "persons/<Name>/1.jpg" exists).
func loadItems(csvData []byte, images fs.FS) ([]*Item, error) {
	r := csv.NewReader(bytes.NewReader(csvData))
	head, err := r.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	for _, c := range []string{"index", "category", "item", "word", "gender"} {
		if _, ok := col[c]; !ok {
			return nil, fmt.Errorf("items.csv: column %q missing", c)
		}
	}
	var items []*Item
	for line := 2; ; line++ {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		idx, err := strconv.Atoi(rec[col["index"]])
		if err != nil {
			return nil, fmt.Errorf("items.csv line %d: index: %w", line, err)
		}
		it := &Item{Index: idx, Category: rec[col["category"]], Name: rec[col["item"]],
			Word: rec[col["word"]], Gender: rec[col["gender"]]}
		if it.Category != "person" && it.Category != "place" {
			return nil, fmt.Errorf("items.csv line %d: category %q", line, it.Category)
		}
		if idx != len(items)+1 {
			return nil, fmt.Errorf("items.csv line %d: index %d, expected %d", line, idx, len(items)+1)
		}
		dir := path.Join(it.folder(), it.Name)
		ents, err := fs.ReadDir(images, dir)
		if err != nil {
			return nil, fmt.Errorf("photos of %q: %w", it.Name, err)
		}
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".jpg") {
				it.Images = append(it.Images, path.Join(dir, e.Name()))
			}
		}
		sort.Strings(it.Images)
		if len(it.Images) == 0 {
			return nil, fmt.Errorf("no photo for %q in %s", it.Name, dir)
		}
		items = append(items, it)
	}
	nPers := 0
	for _, it := range items {
		if it.Category == "person" {
			nPers++
		}
	}
	if len(items) != 32 || nPers != 16 {
		return nil, fmt.Errorf("items.csv: %d items (%d persons), expected 32 (16 persons, then 16 places)", len(items), nPers)
	}
	for i, it := range items {
		if (i < 16) != (it.Category == "person") {
			return nil, fmt.Errorf("items.csv: the 16 persons must come first (index 1-16), then the 16 places")
		}
	}
	return items, nil
}

// firstFormat: the format of trial 1 of a run, alternated across subjects and
// across the two runs of a subject.
func firstFormat(subject, run int) string {
	if (subject+run)%2 == 0 {
		return "image"
	}
	return "word"
}

func newRNG(subject, run, salt int) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(subject)*1000+uint64(salt), uint64(run)))
}

// buildRun returns the trials of one run (32 items x 2 formats x repsPerRun =
// 384), deterministic for a given subject and run:
//   - image and word strictly alternate;
//   - each item appears repsPerRun times as an image and repsPerRun times as a word;
//   - the same item never comes back within 2 trials (so never twice in a row,
//     and never twice in a row within one format);
//   - at most maxSameCat persons (or places) in a row;
//   - photos of an item are used in turn over the session (balanced);
//   - targetsPerRun red frames, see placeTargets.
func buildRun(items []*Item, subject, run int) ([]*Trial, error) {
	rng := newRNG(subject, run, 1)
	n := len(items) * 2 * repsPerRun
	first := firstFormat(subject, run)
	var seq []*Trial
	for attempt := 0; attempt < 2000 && seq == nil; attempt++ {
		seq = tryOrder(items, n, first, rng)
	}
	if seq == nil {
		return nil, fmt.Errorf("run %d: no order satisfies the constraints", run)
	}
	// Photos: item k's j-th image presentation of the session uses photo
	// perm[j mod n], perm fixed per subject and item.
	seen := map[int]int{}
	for _, t := range seq {
		t.Run = run
		if t.Format != "image" {
			continue
		}
		it := t.Item
		perm := newRNG(subject, 0, 100+it.Index).Perm(len(it.Images))
		j := (run-1)*repsPerRun + seen[it.Index]
		seen[it.Index]++
		t.Image = it.Images[perm[j%len(perm)]]
	}
	for i, t := range seq {
		t.Number = i + 1
	}
	if err := placeTargets(seq, rng, targetsPerRun); err != nil {
		return nil, fmt.Errorf("run %d: %w", run, err)
	}
	return seq, nil
}

// tryOrder fills the n positions one at a time, picking at random among the
// remaining presentations of the required format that respect the
// constraints; nil when it gets stuck (the caller retries).
func tryOrder(items []*Item, n int, first string, rng *rand.Rand) []*Trial {
	left := map[string][]int{"image": {}, "word": {}} // remaining count per item index-1
	for _, f := range []string{"image", "word"} {
		left[f] = make([]int, len(items))
		for i := range items {
			left[f][i] = n / 2 / len(items)
		}
	}
	seq := make([]*Trial, 0, n)
	for pos := 0; pos < n; pos++ {
		f := first
		if pos%2 == 1 {
			f = map[string]string{"image": "word", "word": "image"}[first]
		}
		var cands []int
		var weights []int
		total := 0
		for i, c := range left[f] {
			if c == 0 {
				continue
			}
			it := items[i]
			if pos >= 1 && seq[pos-1].Item == it || pos >= 2 && seq[pos-2].Item == it {
				continue
			}
			if pos >= maxSameCat {
				same := true
				for k := 1; k <= maxSameCat; k++ {
					if seq[pos-k].Item.Category != it.Category {
						same = false
						break
					}
				}
				if same {
					continue
				}
			}
			cands = append(cands, i)
			weights = append(weights, c) // favour items with more left: avoids getting stuck at the end
			total += c
		}
		if len(cands) == 0 {
			return nil
		}
		x := rng.IntN(total)
		pick := cands[len(cands)-1]
		for k, w := range weights {
			if x < w {
				pick = cands[k]
				break
			}
			x -= w
		}
		left[f][pick]--
		seq = append(seq, &Trial{Format: f, Item: items[pick]})
	}
	return seq
}

// placeTargets marks k trials as red-frame targets: half images half words,
// and within each format half persons half places; none in the first
// targetMinStart trials or the last 3, and at least targetMinGap trials apart.
func placeTargets(seq []*Trial, rng *rand.Rand, k int) error {
	type cell struct{ format, cat string }
	quota := map[cell]int{}
	cells := []cell{{"image", "person"}, {"image", "place"}, {"word", "person"}, {"word", "place"}}
	for i := 0; i < k; i++ {
		quota[cells[i%4]]++
	}
	for attempt := 0; attempt < 5000; attempt++ {
		for _, t := range seq {
			t.Target = false
		}
		need := map[cell]int{}
		for c, q := range quota {
			need[c] = q
		}
		order := rng.Perm(len(seq))
		var chosen []int
		for _, p := range order {
			if p < targetMinStart || p >= len(seq)-3 {
				continue
			}
			c := cell{seq[p].Format, seq[p].Item.Category}
			if need[c] == 0 {
				continue
			}
			ok := true
			for _, q := range chosen {
				if abs(p-q) < targetMinGap {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			chosen = append(chosen, p)
			need[c]--
			if len(chosen) == k {
				break
			}
		}
		if len(chosen) == k {
			for _, p := range chosen {
				seq[p].Target = true
			}
			return nil
		}
	}
	return fmt.Errorf("cannot place %d targets", k)
}

// buildPractice: a short sequence for the instructions (no triggers, not
// analysed): 16 trials, alternating formats, 3 red frames.
func buildPractice(items []*Item) []*Trial {
	rng := newRNG(0, 0, 7)
	perm := rng.Perm(len(items))
	var seq []*Trial
	for i := 0; i < 16; i++ {
		it := items[perm[i]]
		f := "image"
		if i%2 == 1 {
			f = "word"
		}
		t := &Trial{Number: i + 1, Format: f, Item: it, Target: i == 3 || i == 8 || i == 13}
		if f == "image" {
			t.Image = it.Images[rng.IntN(len(it.Images))]
		}
		seq = append(seq, t)
	}
	return seq
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
