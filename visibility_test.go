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
	"encoding/json"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2/index/scorch"
)

func newTestScorch(t *testing.T, path string) Index {
	t.Helper()
	idx, err := NewUsing(path, NewIndexMapping(), scorch.Name, scorch.Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func receiptFor(idx Index, epoch uint64) *WriteReceipt {
	return &WriteReceipt{Points: map[string]uint64{idx.Name(): epoch}}
}

func indexDoc(t *testing.T, idx Index, id string) {
	t.Helper()
	if err := idx.Index(id, map[string]interface{}{"name": id}); err != nil {
		t.Fatal(err)
	}
}

// waitBlocksThen verifies the wait is parked, runs trigger, and asserts
// the wait returns without error.
func waitBlocksThen(t *testing.T, ri ReceiptIndex, target *WriteReceipt,
	triggers ...func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ri.WaitForVisibility(ctx, target) }()

	for i, trigger := range triggers {
		select {
		case err := <-done:
			t.Fatalf("wait returned after %d triggers, err: %v", i, err)
		case <-time.After(200 * time.Millisecond):
		}
		trigger()
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait err: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait was not unblocked")
	}
}

func nameQuery(name string) *SearchRequest {
	q := NewTermQuery(name)
	q.SetField("name")
	return NewSearchRequestOptions(q, 10, 0, false)
}

func TestWriteReceiptStringAndJSON(t *testing.T) {
	r := newWriteReceipt("idx", 7)
	if r.String() != "idx@7" {
		t.Fatalf("unexpected string: %s", r.String())
	}
	bytes, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var got WriteReceipt
	if err = json.Unmarshal(bytes, &got); err != nil {
		t.Fatal(err)
	}
	if got.Points["idx"] != 7 {
		t.Fatalf("json round trip mismatch: %v", got.Points)
	}
}

func TestWriteReceiptScorchMemory(t *testing.T) {
	idx := newTestScorch(t, "")
	defer func() {
		if err := idx.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	ri, ok := idx.(ReceiptIndex)
	if !ok {
		t.Fatal("mem scorch does not implement ReceiptIndex")
	}

	batch := idx.NewBatch()
	if err := batch.Index("doc1", map[string]interface{}{"name": "doc1"}); err != nil {
		t.Fatal(err)
	}
	receipt, err := ri.BatchWithReceipt(batch)
	if err != nil {
		t.Fatal(err)
	}
	if visible, err := ri.IsVisible(receipt); err != nil || !visible {
		t.Fatalf("expected visible, got %v err %v", visible, err)
	}

	res, err := ri.SearchWithReceipt(nameQuery("doc1"), receipt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 {
		t.Fatalf("expected 1 hit, got %d", res.Total)
	}

	target := receiptFor(idx, receipt.Points[idx.Name()]+2)
	waitBlocksThen(t, ri, target,
		func() { indexDoc(t, idx, "doc2") },
		func() { indexDoc(t, idx, "doc3") })

	delReceipt, err := ri.DeleteWithReceipt("doc1")
	if err != nil {
		t.Fatal(err)
	}
	res, err = ri.SearchWithReceipt(nameQuery("doc1"), delReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 0 {
		t.Fatalf("expected doc deleted, got %d hits", res.Total)
	}
}

func TestWriteReceiptScorchDisk(t *testing.T) {
	path := createTmpIndexPath(t)
	defer cleanupTmpIndexPath(t, path)

	idx := newTestScorch(t, path)
	ri := idx.(ReceiptIndex)
	defer func() {
		if err := idx.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	receipt, err := ri.IndexWithReceipt("doc1",
		map[string]interface{}{"name": "doc1"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := ri.SearchInContextWithReceipt(ctx, nameQuery("doc1"), receipt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 {
		t.Fatalf("expected 1 hit, got %d", res.Total)
	}
}

func TestWriteReceiptScorchReopen(t *testing.T) {
	path := createTmpIndexPath(t)
	defer cleanupTmpIndexPath(t, path)

	idx := newTestScorch(t, path)
	ri := idx.(ReceiptIndex)
	receipt, err := ri.BatchWithReceipt(func() *Batch {
		b := idx.NewBatch()
		if err = b.Index("doc1", map[string]interface{}{"name": "doc1"}); err != nil {
			t.Fatal(err)
		}
		return b
	}())
	if err != nil {
		t.Fatal(err)
	}
	if visible, err := ri.IsVisible(receipt); err != nil || !visible {
		t.Fatalf("expected visible before close, got %v err %v", visible, err)
	}
	if err = idx.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	ri2 := reopened.(ReceiptIndex)

	if visible, err := ri2.IsVisible(receipt); err != nil || !visible {
		t.Fatalf("expected visible after reopen, got %v err %v", visible, err)
	}
	res, err := ri2.SearchWithReceipt(nameQuery("doc1"), receipt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 {
		t.Fatalf("expected 1 hit after reopen, got %d", res.Total)
	}
}

func TestWriteReceiptAliasMembers(t *testing.T) {
	path1 := createTmpIndexPath(t)
	defer cleanupTmpIndexPath(t, path1)
	path2 := createTmpIndexPath(t)
	defer cleanupTmpIndexPath(t, path2)

	idx1 := newTestScorch(t, path1)
	defer func() {
		if err := idx1.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	idx2 := newTestScorch(t, path2)
	defer func() {
		if err := idx2.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	alias := NewIndexAlias(idx1, idx2)
	indexDoc(t, idx2, "in2")

	baseline, err := idx1.(ReceiptIndex).BatchWithReceipt(idx1.NewBatch())
	if err != nil {
		t.Fatal(err)
	}
	// a safe disk batch advances through introduction and the persisted
	// segment swap, so one batch reaches baseline+1
	target := receiptFor(idx1, baseline.Points[idx1.Name()]+1)
	waitBlocksThen(t, alias, target,
		func() { indexDoc(t, idx1, "in1") })

	res, err := alias.SearchWithReceipt(nameQuery("in1"), target)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 {
		t.Fatalf("expected member1 doc through alias, got %d", res.Total)
	}
	res, err = alias.Search(nameQuery("in2"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 {
		t.Fatalf("expected member2 doc through alias, got %d", res.Total)
	}

	unknown := &WriteReceipt{Points: map[string]uint64{"not-a-member": 1}}
	if err = alias.WaitForVisibility(context.Background(), unknown); err != ErrorReceiptTargetMismatch {
		t.Fatalf("expected mismatch error, got %v", err)
	}
}

func TestWriteReceiptNestedAlias(t *testing.T) {
	path := createTmpIndexPath(t)
	defer cleanupTmpIndexPath(t, path)

	leaf := newTestScorch(t, path)
	defer func() {
		if err := leaf.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	inner := NewIndexAlias(leaf)
	outer := NewIndexAlias(inner)

	baseline, err := leaf.(ReceiptIndex).BatchWithReceipt(leaf.NewBatch())
	if err != nil {
		t.Fatal(err)
	}
	target := receiptFor(leaf, baseline.Points[leaf.Name()]+1)
	waitBlocksThen(t, outer, target,
		func() { indexDoc(t, leaf, "doc1") })

	visible, err := outer.IsVisible(target)
	if err != nil || !visible {
		t.Fatalf("expected visible through nested alias, got %v err %v", visible, err)
	}
}

func TestWriteReceiptAliasSingleWrites(t *testing.T) {
	path := createTmpIndexPath(t)
	defer cleanupTmpIndexPath(t, path)

	idx := newTestScorch(t, path)
	defer func() {
		if err := idx.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	single := NewIndexAlias(idx)
	if _, err := single.BatchWithReceipt(single.NewBatch()); err != nil {
		t.Fatal(err)
	}

	path2 := createTmpIndexPath(t)
	defer cleanupTmpIndexPath(t, path2)
	idx2 := newTestScorch(t, path2)
	defer func() {
		if err := idx2.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	multi := NewIndexAlias(idx, idx2)
	if _, err := multi.IndexWithReceipt("x", nil); err != ErrorAliasMulti {
		t.Fatalf("expected alias multi error, got %v", err)
	}
}

func TestWriteReceiptUpsideDown(t *testing.T) {
	idx, err := NewMemOnly(NewIndexMapping())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := idx.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	ri := idx.(ReceiptIndex)

	receipt, err := ri.IndexWithReceipt("doc1",
		map[string]interface{}{"name": "doc1"})
	if err != nil {
		t.Fatal(err)
	}
	if visible, err := ri.IsVisible(receipt); err != nil || !visible {
		t.Fatalf("expected visible, got %v err %v", visible, err)
	}

	target := receiptFor(idx, receipt.Points[idx.Name()]+1)
	waitBlocksThen(t, ri, target,
		func() { indexDoc(t, idx, "doc2") })
}
