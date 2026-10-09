//  Copyright (c) 2026 Couchbase, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 		http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bleve

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/blevesearch/bleve/v2/index/scorch"
	index "github.com/blevesearch/bleve_index_api"
)

func newVersionTestIndex(t *testing.T) (Index, string) {
	t.Helper()
	path := createTmpIndexPath(t)
	idx, err := New(path, NewIndexMapping())
	if err != nil {
		t.Fatal(err)
	}
	return idx, path
}

func docNamed(t *testing.T, idx Index, id string) string {
	t.Helper()
	doc, err := idx.Document(id)
	if err != nil {
		t.Fatal(err)
	}
	if doc == nil {
		t.Fatalf("expected document %q, missing", id)
	}
	var got string
	doc.VisitFields(func(f index.Field) {
		if f.Name() == "name" {
			if tf, ok := f.(index.TextField); ok {
				got = tf.Text()
			}
		}
	})
	return got
}

func expectVersion(t *testing.T, vi VersionedIndex, id string,
	wantVersion uint64, wantExists bool) {
	t.Helper()
	v, exists, err := vi.Version(id)
	if err != nil {
		t.Fatal(err)
	}
	if exists != wantExists {
		t.Fatalf("doc %q exists: want %v, got %v", id, wantExists, exists)
	}
	if v != wantVersion {
		t.Fatalf("doc %q version: want %d, got %d", id, wantVersion, v)
	}
}

func expectConflict(t *testing.T, err error, id string,
	expected, current uint64) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a version conflict error, got nil")
	}
	if !errors.Is(err, ErrorVersionConflict) {
		t.Fatalf("expected ErrorVersionConflict, got %v", err)
	}
	var conflicts VersionConflictErrors
	if !errors.As(err, &conflicts) {
		t.Fatalf("expected VersionConflictErrors, got %T: %v", err, err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d: %v", len(conflicts), conflicts)
	}
	c := conflicts[0]
	if c.DocID != id || c.Expected != expected || c.Current != current {
		t.Fatalf("unexpected conflict entry: %+v", c)
	}
}

func forceMerge(t *testing.T, idx Index) {
	t.Helper()
	adv, err := idx.Advanced()
	if err != nil {
		t.Fatal(err)
	}
	s, ok := adv.(*scorch.Scorch)
	if !ok {
		t.Fatalf("expected scorch index, got %T", adv)
	}
	if err = s.ForceMerge(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestVersionedConditionalIndex(t *testing.T) {
	idx, path := newVersionTestIndex(t)
	defer cleanupTmpIndexPath(t, path)
	defer idx.Close()

	vi, ok := idx.(VersionedIndex)
	if !ok {
		t.Fatalf("index does not implement VersionedIndex")
	}

	if err := vi.IndexWithVersion("a",
		map[string]interface{}{"name": "one"}, 0, 1); err != nil {
		t.Fatal(err)
	}
	expectVersion(t, vi, "a", 1, true)
	if got := docNamed(t, idx, "a"); got != "one" {
		t.Fatalf("expected name one, got %s", got)
	}

	err := vi.IndexWithVersion("a",
		map[string]interface{}{"name": "stale-create"}, 0, 5)
	expectConflict(t, err, "a", 0, 1)
	if got := docNamed(t, idx, "a"); got != "one" {
		t.Fatalf("stale create altered content: got %s", got)
	}

	if err = vi.IndexWithVersion("a",
		map[string]interface{}{"name": "two"}, 1, 2); err != nil {
		t.Fatal(err)
	}
	expectVersion(t, vi, "a", 2, true)

	err = vi.IndexWithVersion("a",
		map[string]interface{}{"name": "stale-update"}, 1, 3)
	expectConflict(t, err, "a", 1, 2)
	if got := docNamed(t, idx, "a"); got != "two" {
		t.Fatalf("stale update altered content: got %s", got)
	}
}

func TestVersionedConditionalDelete(t *testing.T) {
	idx, path := newVersionTestIndex(t)
	defer cleanupTmpIndexPath(t, path)
	defer idx.Close()

	vi := idx.(VersionedIndex)

	if err := vi.IndexWithVersion("a",
		map[string]interface{}{"name": "one"}, 0, 1); err != nil {
		t.Fatal(err)
	}

	err := vi.DeleteWithVersion("a", 2)
	expectConflict(t, err, "a", 2, 1)
	expectVersion(t, vi, "a", 1, true)

	if err = vi.DeleteWithVersion("a", 1); err != nil {
		t.Fatal(err)
	}
	expectVersion(t, vi, "a", 0, false)

	if err = vi.DeleteWithVersion("a", 0); err != nil {
		t.Fatalf("conditional delete of absent doc expecting 0: %v", err)
	}
	err = vi.DeleteWithVersion("a", 1)
	expectConflict(t, err, "a", 1, 0)

	if err = vi.IndexWithVersion("a",
		map[string]interface{}{"name": "two"}, 0, 2); err != nil {
		t.Fatal(err)
	}
	expectVersion(t, vi, "a", 2, true)
}

func TestVersionedBatch(t *testing.T) {
	idx, path := newVersionTestIndex(t)
	defer cleanupTmpIndexPath(t, path)
	defer idx.Close()

	vi := idx.(VersionedIndex)

	b := idx.NewBatch()
	if err := b.IndexWithVersion("a",
		map[string]interface{}{"name": "a1"}, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := b.IndexWithVersion("b",
		map[string]interface{}{"name": "b1"}, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := idx.Batch(b); err != nil {
		t.Fatal(err)
	}
	expectVersion(t, vi, "a", 1, true)
	expectVersion(t, vi, "b", 1, true)

	b2 := idx.NewBatch()
	if err := b2.IndexWithVersion("a",
		map[string]interface{}{"name": "a2"}, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := b2.IndexWithVersion("b",
		map[string]interface{}{"name": "b2"}, 9, 2); err != nil {
		t.Fatal(err)
	}
	err := idx.Batch(b2)
	expectConflict(t, err, "b", 9, 1)
	expectVersion(t, vi, "a", 1, true)
	if got := docNamed(t, idx, "a"); got != "a1" {
		t.Fatalf("atomic batch partially applied, a name = %s", got)
	}
	if got := docNamed(t, idx, "b"); got != "b1" {
		t.Fatalf("atomic batch partially applied, b name = %s", got)
	}

	b3 := idx.NewBatch()
	if err := b3.IndexWithVersion("a",
		map[string]interface{}{"name": "a2"}, 7, 2); err != nil {
		t.Fatal(err)
	}
	b3.DeleteWithVersion("b", 7)
	err = idx.Batch(b3)
	if !errors.Is(err, ErrorVersionConflict) {
		t.Fatalf("expected ErrorVersionConflict, got %v", err)
	}
	var conflicts VersionConflictErrors
	if !errors.As(err, &conflicts) || len(conflicts) != 2 {
		t.Fatalf("expected 2 conflicts, got %v", err)
	}
	if conflicts[0].DocID != "a" || conflicts[1].DocID != "b" {
		t.Fatalf("conflicts not reported in sorted doc order: %v", conflicts)
	}

	b4 := idx.NewBatch()
	b4.Index("c", map[string]interface{}{"name": "c1"})
	if err := b4.IndexWithVersion("a",
		map[string]interface{}{"name": "a2"}, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := idx.Batch(b4); err != nil {
		t.Fatal(err)
	}
	if got := docNamed(t, idx, "c"); got != "c1" {
		t.Fatalf("expected c1, got %s", got)
	}
	expectVersion(t, vi, "a", 2, true)

	b5 := idx.NewBatch()
	b5.Index("d", map[string]interface{}{"name": "d1"})
	b5.DeleteWithVersion("b", 3)
	err = idx.Batch(b5)
	expectConflict(t, err, "b", 3, 1)
	doc, err := idx.Document("d")
	if err != nil {
		t.Fatal(err)
	}
	if doc != nil {
		t.Fatalf("unconditional op committed despite conditional conflict")
	}
}

func TestVersionedReopen(t *testing.T) {
	idx, path := newVersionTestIndex(t)
	defer cleanupTmpIndexPath(t, path)

	vi := idx.(VersionedIndex)
	b := idx.NewBatch()
	if err := b.IndexWithVersion("a",
		map[string]interface{}{"name": "a1"}, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := b.IndexWithVersion("b",
		map[string]interface{}{"name": "b1"}, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := idx.Batch(b); err != nil {
		t.Fatal(err)
	}
	if err := vi.IndexWithVersion("a",
		map[string]interface{}{"name": "a2"}, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := idx.Close(); err != nil {
		t.Fatal(err)
	}

	idx2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer idx2.Close()

	vi2 := idx2.(VersionedIndex)
	expectVersion(t, vi2, "a", 2, true)
	expectVersion(t, vi2, "b", 1, true)

	err = vi2.IndexWithVersion("a",
		map[string]interface{}{"name": "stale"}, 1, 3)
	expectConflict(t, err, "a", 1, 2)

	if err = vi2.IndexWithVersion("a",
		map[string]interface{}{"name": "a3"}, 2, 3); err != nil {
		t.Fatal(err)
	}
	expectVersion(t, vi2, "a", 3, true)
}

func TestVersionedMerge(t *testing.T) {
	idx, path := newVersionTestIndex(t)
	defer cleanupTmpIndexPath(t, path)
	defer idx.Close()

	vi := idx.(VersionedIndex)
	for _, n := range []string{"a", "b", "c"} {
		b := idx.NewBatch()
		if err := b.IndexWithVersion(n,
			map[string]interface{}{"name": n + "1"}, 0, 1); err != nil {
			t.Fatal(err)
		}
		if err := idx.Batch(b); err != nil {
			t.Fatal(err)
		}
	}

	forceMerge(t, idx)
	for _, n := range []string{"a", "b", "c"} {
		expectVersion(t, vi, n, 1, true)
		if got := docNamed(t, idx, n); got != n+"1" {
			t.Fatalf("after merge, doc %s name = %s", n, got)
		}
	}

	if err := vi.IndexWithVersion("a",
		map[string]interface{}{"name": "a2"}, 1, 2); err != nil {
		t.Fatal(err)
	}
	err := vi.IndexWithVersion("b",
		map[string]interface{}{"name": "stale"}, 2, 3)
	expectConflict(t, err, "b", 2, 1)

	if err = vi.DeleteWithVersion("c", 1); err != nil {
		t.Fatal(err)
	}
	expectVersion(t, vi, "c", 0, false)

	forceMerge(t, idx)
	expectVersion(t, vi, "a", 2, true)
	expectVersion(t, vi, "b", 1, true)
	expectVersion(t, vi, "c", 0, false)
	if got := docNamed(t, idx, "a"); got != "a2" {
		t.Fatalf("after second merge, a name = %s", got)
	}
}

func TestUnversionedWritesUnchanged(t *testing.T) {
	idx, path := newVersionTestIndex(t)
	defer cleanupTmpIndexPath(t, path)
	defer idx.Close()

	vi := idx.(VersionedIndex)

	if err := idx.Index("a", map[string]interface{}{"name": "one"}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Index("a", map[string]interface{}{"name": "two"}); err != nil {
		t.Fatal(err)
	}
	if got := docNamed(t, idx, "a"); got != "two" {
		t.Fatalf("plain Index overwrite broken, got %s", got)
	}
	v, exists, err := vi.Version("a")
	if err != nil {
		t.Fatal(err)
	}
	if !exists || v != 0 {
		t.Fatalf("plain indexed doc expected version 0/exists, got %d/%v", v, exists)
	}

	err = vi.IndexWithVersion("a",
		map[string]interface{}{"name": "three"}, 0, 1)
	expectConflict(t, err, "a", 0, 0)
	if got := docNamed(t, idx, "a"); got != "two" {
		t.Fatalf("conflict altered content, got %s", got)
	}

	if err = idx.Delete("a"); err != nil {
		t.Fatal(err)
	}
	doc, err := idx.Document("a")
	if err != nil {
		t.Fatal(err)
	}
	if doc != nil {
		t.Fatal("plain Delete broken")
	}

	b := idx.NewBatch()
	b.Index("x", map[string]interface{}{"name": "x1"})
	b.Index("y", map[string]interface{}{"name": "y1"})
	b.Delete("x")
	if err = idx.Batch(b); err != nil {
		t.Fatal(err)
	}
	docX, err := idx.Document("x")
	if err != nil {
		t.Fatal(err)
	}
	if docX != nil {
		t.Fatal("plain batch delete broken")
	}
	if got := docNamed(t, idx, "y"); got != "y1" {
		t.Fatalf("plain batch index broken, got %s", got)
	}

	if err = vi.IndexWithVersion("z",
		map[string]interface{}{"name": "z1"}, 0, 1); err != nil {
		t.Fatal(err)
	}
	expectVersion(t, vi, "z", 1, true)
}

func TestVersionedConcurrentWriters(t *testing.T) {
	idx, path := newVersionTestIndex(t)
	defer cleanupTmpIndexPath(t, path)
	defer idx.Close()

	vi := idx.(VersionedIndex)
	if err := vi.IndexWithVersion("counter",
		map[string]interface{}{"name": "v1"}, 0, 1); err != nil {
		t.Fatal(err)
	}

	const writers = 8
	const iterations = 25
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				for {
					cur, exists, err := vi.Version("counter")
					if err != nil {
						t.Errorf("version read: %v", err)
						return
					}
					if !exists {
						t.Errorf("counter disappeared")
						return
					}
					err = vi.IndexWithVersion("counter",
						map[string]interface{}{
							"name": fmt.Sprintf("v%d", cur+1),
						}, cur, cur+1)
					if err == nil {
						break
					}
					if !errors.Is(err, ErrorVersionConflict) {
						t.Errorf("unexpected error: %v", err)
						return
					}
				}
			}
		}()
	}
	wg.Wait()

	expectVersion(t, vi, "counter", 1+writers*iterations, true)
	if got := docNamed(t, idx, "counter"); got !=
		fmt.Sprintf("v%d", 1+writers*iterations) {
		t.Fatalf("lost update detected, counter = %s", got)
	}
}

func TestVersionedEmptyID(t *testing.T) {
	idx, path := newVersionTestIndex(t)
	defer cleanupTmpIndexPath(t, path)
	defer idx.Close()

	vi := idx.(VersionedIndex)
	if err := vi.IndexWithVersion("",
		map[string]interface{}{"name": "x"}, 0, 1); err != ErrorEmptyID {
		t.Fatalf("expected ErrorEmptyID, got %v", err)
	}
	if err := vi.DeleteWithVersion("", 0); err != ErrorEmptyID {
		t.Fatalf("expected ErrorEmptyID, got %v", err)
	}

	b := idx.NewBatch()
	if err := b.IndexWithVersion("",
		map[string]interface{}{"name": "x"}, 0, 1); err != ErrorEmptyID {
		t.Fatalf("expected ErrorEmptyID, got %v", err)
	}
}
