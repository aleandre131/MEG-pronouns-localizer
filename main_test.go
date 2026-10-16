package main

import (
	"io/fs"
	"testing"
)

func testItems(t *testing.T) []*Item {
	t.Helper()
	sub, err := fs.Sub(imagesFS, "assets/images")
	if err != nil {
		t.Fatal(err)
	}
	items, err := loadItems(itemsCSV, sub)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestItemsAndPhotos(t *testing.T) {
	items := testItems(t)
	total := 0
	for _, it := range items {
		if len(it.Images) < 3 {
			t.Errorf("%s: %d photos", it.Name, len(it.Images))
		}
		total += len(it.Images)
	}
	t.Logf("32 items, %d photos", total)
}

func TestRunsRespectTheDesign(t *testing.T) {
	items := testItems(t)
	for subject := 1; subject <= 40; subject++ {
		photoUse := map[string]int{}
		for run := 1; run <= nRuns; run++ {
			seq, err := buildRun(items, subject, run)
			if err != nil {
				t.Fatal(err)
			}
			if len(seq) != 384 {
				t.Fatalf("sub %d run %d: %d trials", subject, run, len(seq))
			}
			if seq[0].Format != firstFormat(subject, run) {
				t.Errorf("sub %d run %d: first format", subject, run)
			}
			count := map[[2]int]int{}
			targets := map[string]int{}
			lastTarget := -100
			for i, tr := range seq {
				if i > 0 && tr.Format == seq[i-1].Format {
					t.Fatalf("sub %d run %d trial %d: formats do not alternate", subject, run, i+1)
				}
				if i >= 1 && seq[i-1].Item == tr.Item || i >= 2 && seq[i-2].Item == tr.Item {
					t.Fatalf("sub %d run %d trial %d: %s repeated too soon", subject, run, i+1, tr.Item.Name)
				}
				if i >= maxSameCat {
					same := true
					for k := 1; k <= maxSameCat; k++ {
						same = same && seq[i-k].Item.Category == tr.Item.Category
					}
					if same {
						t.Fatalf("sub %d run %d trial %d: more than %d %s in a row", subject, run, i+1, maxSameCat, tr.Item.Category)
					}
				}
				f := 0
				if tr.Format == "word" {
					f = 1
				}
				count[[2]int{tr.Item.Index, f}]++
				if (tr.Format == "image") != (tr.Image != "") {
					t.Fatalf("image field on trial %d", i+1)
				}
				photoUse[tr.Image]++
				if tr.Target {
					targets[tr.Format+"/"+tr.Item.Category]++
					if i < targetMinStart || i-lastTarget < targetMinGap {
						t.Fatalf("sub %d run %d: target at trial %d too early/close", subject, run, i+1)
					}
					lastTarget = i
				}
				if tr.Number != i+1 || tr.Run != run {
					t.Fatal("numbering")
				}
			}
			for _, c := range count {
				if c != repsPerRun {
					t.Fatalf("sub %d run %d: unbalanced item counts %v", subject, run, count)
				}
			}
			for k, v := range targets {
				if v != targetsPerRun/4 {
					t.Fatalf("sub %d run %d: targets %v (%s)", subject, run, targets, k)
				}
			}
		}
		// Over the session, an item's photos are used as evenly as possible.
		for _, it := range items {
			min, max := 1000, 0
			for _, p := range it.Images {
				u := photoUse[p]
				if u < min {
					min = u
				}
				if u > max {
					max = u
				}
			}
			if max-min > 1 {
				t.Fatalf("sub %d: photos of %s used unevenly (%d-%d)", subject, it.Name, min, max)
			}
		}
	}
}

func TestSameSubjectSameOrder(t *testing.T) {
	items := testItems(t)
	a, _ := buildRun(items, 3, 1)
	b, _ := buildRun(items, 3, 1)
	c, _ := buildRun(items, 4, 1)
	same := true
	for i := range a {
		if a[i].Item != b[i].Item || a[i].Target != b[i].Target || a[i].Image != b[i].Image {
			t.Fatal("same subject and run must give the same order")
		}
		same = same && a[i].Item == c[i].Item
	}
	if same {
		t.Fatal("two subjects got the same order")
	}
}

func TestTriggerCodes(t *testing.T) {
	items := testItems(t)
	seen := map[byte]string{}
	for _, it := range items {
		for _, f := range []string{"image", "word"} {
			for _, tg := range []bool{false, true} {
				tr := &Trial{Format: f, Item: it, Target: tg}
				c := tr.Code()
				if c == 0 || c >= codeRunStart {
					t.Fatalf("code %d out of range", c)
				}
				if prev, ok := seen[c]; ok {
					t.Fatalf("code %d used twice (%s)", c, prev)
				}
				seen[c] = it.Name + f
			}
		}
	}
	if (&Trial{Format: "image", Item: items[0]}).Code() != 1 ||
		(&Trial{Format: "word", Item: items[31]}).Code() != 64 ||
		(&Trial{Format: "image", Item: items[16], Target: true}).Code() != 81 ||
		(&Trial{Format: "word", Item: items[31], Target: true}).Code() != 128 {
		t.Fatal("code table changed")
	}
}
