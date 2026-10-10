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

package searcher

import (
	"reflect"

	"github.com/blevesearch/bleve/v2/search"
	"github.com/blevesearch/bleve/v2/size"
	index "github.com/blevesearch/bleve_index_api"
)

var reflectStaticSizeNamedSearcher int

func init() {
	var ns NamedSearcher
	reflectStaticSizeNamedSearcher = int(reflect.TypeOf(ns).Size())
}

// NamedSearcher wraps another searcher and stamps a stable query name onto
// every DocumentMatch it emits. It is how optional clause names reach the
// hit's matched-name set through arbitrarily nested query trees.
//
// NamedSearcher deliberately does not implement index.Optimizable. The
// conjunction/disjunction push-down and "unadorned" optimizations collapse
// all child clauses into a single term-field reader, which makes it
// impossible to tell which individual clauses matched. Keeping the wrapper
// opaque to those optimizations preserves per-clause name identity whenever
// a named clause is present; unnamed queries build no wrapper and keep all of
// their existing optimizations.
type NamedSearcher struct {
	search.Searcher
	name string
}

// NewNamedSearcher wraps s so that every emitted DocumentMatch carries name.
// An empty name or nil searcher is returned unchanged so unnamed searches
// pay no overhead. A MatchNoneSearcher is also returned unwrapped: it can
// never emit a match (so the name would never surface anyway), and keeping
// the bare type preserves callers' existing MatchNone neutrality handling.
func NewNamedSearcher(s search.Searcher, name string) search.Searcher {
	if name == "" || s == nil {
		return s
	}
	if _, ok := s.(*MatchNoneSearcher); ok {
		return s
	}
	return &NamedSearcher{Searcher: s, name: name}
}

func (s *NamedSearcher) Size() int {
	return reflectStaticSizeNamedSearcher + size.SizeOfPtr +
		len(s.name) + s.Searcher.Size()
}

func (s *NamedSearcher) Next(ctx *search.SearchContext) (*search.DocumentMatch, error) {
	rv, err := s.Searcher.Next(ctx)
	if rv != nil {
		rv.AddHitName(s.name)
	}
	return rv, err
}

func (s *NamedSearcher) Advance(ctx *search.SearchContext,
	ID index.IndexInternalID,
) (*search.DocumentMatch, error) {
	rv, err := s.Searcher.Advance(ctx, ID)
	if rv != nil {
		rv.AddHitName(s.name)
	}
	return rv, err
}
