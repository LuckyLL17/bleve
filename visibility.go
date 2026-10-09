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
	"fmt"
	"sort"
	"strings"

	"github.com/blevesearch/bleve/v2/document"
	index "github.com/blevesearch/bleve_index_api"
)

// WriteReceipt is a progress token returned by receipt-aware writes.
// It records the visibility point, per index name, at which the write
// becomes observable by queries.  A receipt from one write may be
// carried by a later Search to block until that point is current, or
// checked with IsVisible without waiting.  In-memory segment
// introductions, persisted snapshot swaps and merges all advance the
// same point, and reopening an index continues the same monotonic
// sequence.
type WriteReceipt struct {
	Points map[string]uint64 `json:"points"`
}

func newWriteReceipt(indexName string, epoch uint64) *WriteReceipt {
	return &WriteReceipt{
		Points: map[string]uint64{indexName: epoch},
	}
}

// String prints the receipt points, ordered by index name, in the
// name@epoch form.
func (r *WriteReceipt) String() string {
	if r == nil || len(r.Points) == 0 {
		return "empty receipt"
	}
	names := make([]string, 0, len(r.Points))
	for name := range r.Points {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%s@%d", name, r.Points[name])
	}
	return strings.Join(parts, ",")
}

// visibilityIndex is implemented by the low-level index types that can
// report progress and apply a batch while returning the reached point.
type visibilityIndex interface {
	CurrentEpoch() uint64
	WaitForEpoch(ctx context.Context, epoch uint64) error
	BatchEpoch(batch *index.Batch) (uint64, error)
}

// ReceiptIndex is an Index that supports receipt-bearing writes and
// queries which can wait on the receipt's visibility point.  Obtain it
// from a regular Index with a type assertion:
//
//	if ri, ok := idx.(bleve.ReceiptIndex); ok {
//	    receipt, err := ri.BatchWithReceipt(batch)
//	    ...
//	}
type ReceiptIndex interface {
	Index

	// IndexWithReceipt behaves like Index but additionally returns a
	// WriteReceipt describing the reached visibility point.
	IndexWithReceipt(id string, data interface{}) (*WriteReceipt, error)
	// DeleteWithReceipt behaves like Delete but additionally returns a
	// WriteReceipt describing the reached visibility point.
	DeleteWithReceipt(id string) (*WriteReceipt, error)
	// BatchWithReceipt behaves like Batch but additionally returns a
	// WriteReceipt describing the reached visibility point.
	BatchWithReceipt(b *Batch) (*WriteReceipt, error)

	// WaitForVisibility blocks until every point in the receipt is
	// observable, the context is cancelled or the index is closed.
	WaitForVisibility(ctx context.Context, receipt *WriteReceipt) error
	// IsVisible reports, without blocking, whether every point in the
	// receipt is observable.
	IsVisible(receipt *WriteReceipt) (bool, error)

	// SearchWithReceipt waits for the receipt's visibility point and
	// then executes the search, so the results observe the write.
	SearchWithReceipt(req *SearchRequest, receipt *WriteReceipt) (*SearchResult, error)
	// SearchInContextWithReceipt is SearchWithReceipt with a context.
	SearchInContextWithReceipt(ctx context.Context, req *SearchRequest,
		receipt *WriteReceipt) (*SearchResult, error)
}

func (i *indexImpl) writeWithEpoch(b *index.Batch) (uint64, error) {
	vi, ok := i.i.(visibilityIndex)
	if !ok {
		return 0, ErrorReceiptNotSupported
	}
	return vi.BatchEpoch(b)
}

// IndexWithReceipt analyzes and indexes the data, returning a receipt
// for the reached visibility point.
func (i *indexImpl) IndexWithReceipt(id string, data interface{}) (*WriteReceipt, error) {
	if id == "" {
		return nil, ErrorEmptyID
	}

	i.mutex.RLock()
	defer i.mutex.RUnlock()

	if !i.open {
		return nil, ErrorIndexClosed
	}

	i.FireIndexEvent()

	doc := document.NewDocument(id)
	err := i.m.MapDocument(doc, data)
	if err != nil {
		return nil, err
	}
	b := index.NewBatch()
	b.Update(doc)
	epoch, err := i.writeWithEpoch(b)
	if err != nil {
		return nil, err
	}
	return newWriteReceipt(i.name, epoch), nil
}

// DeleteWithReceipt deletes the identifier, returning a receipt for the
// reached visibility point.
func (i *indexImpl) DeleteWithReceipt(id string) (*WriteReceipt, error) {
	if id == "" {
		return nil, ErrorEmptyID
	}

	i.mutex.RLock()
	defer i.mutex.RUnlock()

	if !i.open {
		return nil, ErrorIndexClosed
	}

	b := index.NewBatch()
	b.Delete(id)
	epoch, err := i.writeWithEpoch(b)
	if err != nil {
		return nil, err
	}
	return newWriteReceipt(i.name, epoch), nil
}

// BatchWithReceipt executes the batch, returning a receipt for the
// reached visibility point.
func (i *indexImpl) BatchWithReceipt(b *Batch) (*WriteReceipt, error) {
	i.mutex.RLock()
	defer i.mutex.RUnlock()

	if !i.open {
		return nil, ErrorIndexClosed
	}

	epoch, err := i.writeWithEpoch(b.internal)
	if err != nil {
		return nil, err
	}
	return newWriteReceipt(i.name, epoch), nil
}

// WaitForVisibility blocks until the receipt point is observable.
func (i *indexImpl) WaitForVisibility(ctx context.Context, r *WriteReceipt) error {
	i.mutex.RLock()
	defer i.mutex.RUnlock()

	if !i.open {
		return ErrorIndexClosed
	}

	epoch, ok := r.Points[i.name]
	if !ok || len(r.Points) != 1 {
		return ErrorReceiptTargetMismatch
	}
	vi, ok := i.i.(visibilityIndex)
	if !ok {
		return ErrorReceiptNotSupported
	}
	return vi.WaitForEpoch(ctx, epoch)
}

// IsVisible reports whether the receipt point is observable.
func (i *indexImpl) IsVisible(r *WriteReceipt) (bool, error) {
	i.mutex.RLock()
	defer i.mutex.RUnlock()

	if !i.open {
		return false, ErrorIndexClosed
	}

	epoch, ok := r.Points[i.name]
	if !ok || len(r.Points) != 1 {
		return false, ErrorReceiptTargetMismatch
	}
	vi, ok := i.i.(visibilityIndex)
	if !ok {
		return false, ErrorReceiptNotSupported
	}
	return vi.CurrentEpoch() >= epoch, nil
}

// SearchWithReceipt waits for the receipt point and then searches.
func (i *indexImpl) SearchWithReceipt(req *SearchRequest,
	r *WriteReceipt) (*SearchResult, error) {
	return i.SearchInContextWithReceipt(context.Background(), req, r)
}

// SearchInContextWithReceipt waits for the receipt point and then
// searches within the context.
func (i *indexImpl) SearchInContextWithReceipt(ctx context.Context,
	req *SearchRequest, r *WriteReceipt) (*SearchResult, error) {
	if err := i.WaitForVisibility(ctx, r); err != nil {
		return nil, err
	}
	return i.SearchInContext(ctx, req)
}
