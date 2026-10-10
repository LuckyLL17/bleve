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
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/blevesearch/bleve/v2/search"
	"github.com/blevesearch/bleve/v2/search/query"
)

// namedDocs are indexed under the default mapping, whose standard analyzer
// lowercases and tokenizes on whitespace.
var namedDocs = map[string]map[string]interface{}{
	"1": {"desc": "red fish", "category": "alpha"},
	"2": {"desc": "blue fish", "category": "alpha"},
	"3": {"desc": "red bird", "category": "beta"},
	"4": {"desc": "green grass", "category": "beta"},
}

func newNamedTestIndex(t *testing.T) Index {
	t.Helper()
	idx, err := NewMemOnly(NewIndexMapping())
	if err != nil {
		t.Fatal(err)
	}
	batch := idx.NewBatch()
	for id, doc := range namedDocs {
		if err := batch.Index(id, doc); err != nil {
			t.Fatal(err)
		}
	}
	if err := idx.Batch(batch); err != nil {
		t.Fatal(err)
	}
	return idx
}

func namedTerm(term, field, name string) *query.TermQuery {
	q := query.NewTermQuery(term)
	q.SetField(field)
	q.SetName(name)
	return q
}

func hitNamesMap(t *testing.T, res *SearchResult) map[int64][]string {
	t.Helper()
	rv := make(map[int64][]string, len(res.Hits))
	for _, hit := range res.Hits {
		// HitNames must always be sorted, which is what keeps the output
		// deterministic across executions.
		if !sort.StringsAreSorted(hit.HitNames) {
			t.Fatalf("hit %s names not sorted: %v", hit.ID, hit.HitNames)
		}
		var id int64
		for _, r := range hit.ID {
			id = id*10 + int64(r-'0')
		}
		rv[id] = append([]string(nil), hit.HitNames...)
	}
	return rv
}

// A named term clause reports its name only on the documents it matched.
func TestNamedQueryTerm(t *testing.T) {
	idx := newNamedTestIndex(t)
	defer func() { _ = idx.Close() }()

	q := namedTerm("red", "desc", "rule-red")
	req := NewSearchRequest(q)
	req.Size = 10
	res, err := idx.Search(req)
	if err != nil {
		t.Fatal(err)
	}
	names := hitNamesMap(t, res)
	if !reflect.DeepEqual(names[1], []string{"rule-red"}) ||
		!reflect.DeepEqual(names[3], []string{"rule-red"}) {
		t.Fatalf("unexpected names: %v", names)
	}
	if _, ok := names[2]; ok {
		t.Fatalf("doc 2 must not match, got: %v", names)
	}
}

// Names flow through a disjunction and only the disjuncts that actually
// matched are reported; the same name on multiple clauses appears once and
// the resulting set stays sorted.
func TestNamedQueryDisjunction(t *testing.T) {
	idx := newNamedTestIndex(t)
	defer func() { _ = idx.Close() }()

	disj := query.NewDisjunctionQuery([]query.Query{
		namedTerm("red", "desc", "rule-red"),
		namedTerm("fish", "desc", "rule-fish"),
	})
	disj.SetMin(1)

	req := NewSearchRequest(disj)
	req.Size = 10
	res, err := idx.Search(req)
	if err != nil {
		t.Fatal(err)
	}
	names := hitNamesMap(t, res)
	if !reflect.DeepEqual(names[1], []string{"rule-fish", "rule-red"}) {
		t.Fatalf("doc 1: %v", names[1])
	}
	if !reflect.DeepEqual(names[2], []string{"rule-fish"}) {
		t.Fatalf("doc 2: %v", names[2])
	}
	if !reflect.DeepEqual(names[3], []string{"rule-red"}) {
		t.Fatalf("doc 3: %v", names[3])
	}
}

// Repeated use of the same stable name collapses to a single entry.
func TestNamedQueryDuplicateNameCollapses(t *testing.T) {
	idx := newNamedTestIndex(t)
	defer func() { _ = idx.Close() }()

	disj := query.NewDisjunctionQuery([]query.Query{
		namedTerm("red", "desc", "same-rule"),
		namedTerm("fish", "desc", "same-rule"),
	})
	disj.SetMin(1)

	req := NewSearchRequest(disj)
	req.Size = 10
	res, err := idx.Search(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range res.Hits {
		if !reflect.DeepEqual(hit.HitNames, []string{"same-rule"}) {
			t.Fatalf("hit %s: %v", hit.ID, hit.HitNames)
		}
	}
}

// Names propagate through nested boolean queries: must, should and filter
// names are all reported, while a matching must_not clause excludes the
// document outright instead of contributing its name.
func TestNamedQueryNestedBoolean(t *testing.T) {
	idx := newNamedTestIndex(t)
	defer func() { _ = idx.Close() }()

	bq := query.NewBooleanQuery(
		[]query.Query{namedTerm("red", "desc", "must-red")},
		[]query.Query{namedTerm("fish", "desc", "should-fish")},
		[]query.Query{namedTerm("bird", "desc", "mustnot-bird")},
	)
	bq.AddFilter(namedTerm("alpha", "category", "filter-type-a"))
	bq.SetName("outer-bool")

	req := NewSearchRequest(bq)
	req.Size = 10
	res, err := idx.Search(req)
	if err != nil {
		t.Fatal(err)
	}
	names := hitNamesMap(t, res)

	// doc 1 satisfies must + should + filter, avoids must_not.
	if !reflect.DeepEqual(names[1],
		[]string{"filter-type-a", "must-red", "outer-bool", "should-fish"}) {
		t.Fatalf("doc 1: %v", names[1])
	}
	// doc 3 satisfies must and matches must_not, so it must be excluded.
	if _, ok := names[3]; ok {
		t.Fatalf("doc 3 must be excluded by must_not: %v", names)
	}
	// doc 2 misses the must clause, so it must be excluded.
	if _, ok := names[2]; ok {
		t.Fatalf("doc 2 must be excluded by must: %v", names)
	}
	// the must_not name must never surface on any returned hit
	for id, ns := range names {
		for _, n := range ns {
			if n == "mustnot-bird" {
				t.Fatalf("must_not name leaked on doc %d: %v", id, ns)
			}
		}
	}
}

// A filter-only boolean query still identifies which filter rules matched
// even though filters execute without scoring.
func TestNamedQueryFilterOnly(t *testing.T) {
	idx := newNamedTestIndex(t)
	defer func() { _ = idx.Close() }()

	bq := query.NewBooleanQuery(nil, nil, nil)
	bq.AddFilter(namedTerm("alpha", "category", "only-type-a"))

	req := NewSearchRequest(bq)
	req.Size = 10
	res, err := idx.Search(req)
	if err != nil {
		t.Fatal(err)
	}
	names := hitNamesMap(t, res)
	if !reflect.DeepEqual(names[1], []string{"only-type-a"}) ||
		!reflect.DeepEqual(names[2], []string{"only-type-a"}) {
		t.Fatalf("filter names: %v", names)
	}
	if _, ok := names[3]; ok {
		t.Fatalf("doc 3 must be filtered out: %v", names)
	}
	// filter-only matches are driven by a match-all searcher, so verify
	// that naming the filter clause does not change their scores versus
	// the equivalent unnamed query.
	plainBQ := query.NewBooleanQuery(nil, nil, nil)
	plainBQ.AddFilter(func() query.Query {
		c := query.NewTermQuery("alpha")
		c.SetField("category")
		return c
	}())
	plainRes, err := idx.Search(NewSearchRequest(plainBQ))
	if err != nil {
		t.Fatal(err)
	}
	plainScores := map[string]float64{}
	for _, hit := range plainRes.Hits {
		plainScores[hit.ID] = hit.Score
	}
	if len(plainScores) != len(res.Hits) {
		t.Fatalf("hit count changed by naming: %d vs %d", len(plainScores), len(res.Hits))
	}
	for _, hit := range res.Hits {
		if hit.Score != plainScores[hit.ID] {
			t.Fatalf("filter-only hit %s score changed by naming: %f vs %f",
				hit.ID, hit.Score, plainScores[hit.ID])
		}
	}
}

// Names survive analysis-driven expansion into phrase and term (match)
// queries.
func TestNamedQueryPhraseAndMatch(t *testing.T) {
	im := NewIndexMapping()
	dm := NewDocumentMapping()
	tf := NewTextFieldMapping()
	tf.IncludeTermVectors = true
	dm.AddFieldMappingsAt("desc", tf)
	im.AddDocumentMapping("_default", dm)

	idx, err := NewMemOnly(im)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idx.Close() }()
	if err := idx.Index("1", map[string]interface{}{"desc": "red fish swims"}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Index("2", map[string]interface{}{"desc": "fish red swims"}); err != nil {
		t.Fatal(err)
	}

	phraseQ := query.NewPhraseQuery([]string{"red", "fish"}, "desc")
	phraseQ.SetName("phrase-rule")
	res, err := idx.Search(NewSearchRequest(phraseQ))
	if err != nil {
		t.Fatal(err)
	}
	names := hitNamesMap(t, res)
	if !reflect.DeepEqual(names[1], []string{"phrase-rule"}) {
		t.Fatalf("phrase hit: %v", names)
	}
	if _, ok := names[2]; ok {
		t.Fatalf("doc 2 must not match phrase: %v", names)
	}

	matchQ := query.NewMatchQuery("red fish")
	matchQ.SetField("desc")
	matchQ.SetName("match-rule")
	req := NewSearchRequest(matchQ)
	req.Size = 10
	res, err = idx.Search(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range res.Hits {
		if !reflect.DeepEqual(hit.HitNames, []string{"match-rule"}) {
			t.Fatalf("match hit %s: %v", hit.ID, hit.HitNames)
		}
	}
}

// Assigning names must not change scores, explanations, ordering or
// pagination; unnamed searches must keep their JSON shape.
func TestNamedQueryScoreCompat(t *testing.T) {
	idx := newNamedTestIndex(t)
	defer func() { _ = idx.Close() }()

	build := func(named bool) query.Query {
		var r query.Query = namedTerm("red", "desc", "r")
		var f query.Query = namedTerm("fish", "desc", "f")
		if !named {
			r = query.NewTermQuery("red")
			r.(*query.TermQuery).SetField("desc")
			f = query.NewTermQuery("fish")
			f.(*query.TermQuery).SetField("desc")
		}
		return query.NewDisjunctionQuery([]query.Query{r, f})
	}

	run := func(named bool, size, from int, explain bool) *SearchResult {
		req := NewSearchRequest(build(named))
		req.Size = size
		req.From = from
		req.Explain = explain
		res, err := idx.Search(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	namedRes := run(true, 1, 0, true)
	plainRes := run(false, 1, 0, true)
	if namedRes.Total != plainRes.Total {
		t.Fatalf("total changed: %d vs %d", namedRes.Total, plainRes.Total)
	}
	if namedRes.Hits[0].ID != plainRes.Hits[0].ID ||
		namedRes.Hits[0].Score != plainRes.Hits[0].Score {
		t.Fatalf("order/score changed: %v %v", namedRes.Hits[0], plainRes.Hits[0])
	}
	if namedRes.Hits[0].Expl == nil || plainRes.Hits[0].Expl == nil {
		t.Fatal("explanation missing")
	}

	// full result sets must agree on ids and scores
	namedAll := run(true, 10, 0, false)
	plainAll := run(false, 10, 0, false)
	if len(namedAll.Hits) != len(plainAll.Hits) {
		t.Fatal("hit count changed")
	}
	for i := range namedAll.Hits {
		a, b := namedAll.Hits[i], plainAll.Hits[i]
		if a.ID != b.ID || a.Score != b.Score {
			t.Fatalf("hit %d diverged: %s %f vs %s %f", i, a.ID, a.Score, b.ID, b.Score)
		}
	}

	// JSON shape of an unnamed search contains no hit_names key
	raw, err := json.Marshal(plainAll.Hits[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hit_names") {
		t.Fatalf("unnamed hit JSON changed: %s", raw)
	}
	raw, err = json.Marshal(namedAll.Hits[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"hit_names"`) {
		t.Fatalf("named hit JSON missing hit_names: %s", raw)
	}
}

// Names are serialized on query clauses and survive a ParseQuery round
// trip through nested boolean JSON.
func TestNamedQueryJSONRoundTrip(t *testing.T) {
	q := namedTerm("red", "desc", "json-term")
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"name":"json-term"`) {
		t.Fatalf("name not serialized: %s", raw)
	}
	parsed, err := query.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	if nq, ok := parsed.(query.NamedQuery); !ok || nq.Name() != "json-term" {
		t.Fatalf("name lost after parse: %v", parsed)
	}

	boolJSON := `{
		"must":  {"conjuncts": [{"term": "red", "field": "desc", "name": "json-must"}]},
		"should": {"disjuncts": [{"term": "fish", "field": "desc", "name": "json-should"}], "min": 1},
		"must_not": {"disjuncts": [{"term": "bird", "field": "desc", "name": "json-mustnot"}], "min": 1},
		"filter": {"term": "alpha", "field": "category", "name": "json-filter"},
		"name": "json-bool"
	}`
	bq, err := query.ParseQuery([]byte(boolJSON))
	if err != nil {
		t.Fatal(err)
	}
	idx := newNamedTestIndex(t)
	defer func() { _ = idx.Close() }()
	req := NewSearchRequest(bq)
	req.Size = 10
	res, err := idx.Search(req)
	if err != nil {
		t.Fatal(err)
	}
	names := hitNamesMap(t, res)
	if !reflect.DeepEqual(names[1],
		[]string{"json-bool", "json-filter", "json-must", "json-should"}) {
		t.Fatalf("doc 1: %v", names[1])
	}
	if _, ok := names[3]; ok {
		t.Fatalf("doc 3 must be excluded: %v", names)
	}

	// every built-in query type must keep unnamed JSON free of "name"
	unnamed := []query.Query{
		query.NewTermQuery("x"), query.NewMatchQuery("x"),
		query.NewMatchPhraseQuery("x y"), query.NewPhraseQuery([]string{"x"}, "d"),
		query.NewFuzzyQuery("x"), query.NewPrefixQuery("x"), query.NewRegexpQuery("x"),
		query.NewWildcardQuery("x*"), query.NewDocIDQuery([]string{"x"}),
		query.NewBoolFieldQuery(true), query.NewMatchAllQuery(), query.NewMatchNoneQuery(),
		query.NewConjunctionQuery(nil), query.NewDisjunctionQuery(nil),
		query.NewBooleanQuery(nil, nil, nil), query.NewQueryStringQuery("x"),
	}
	for _, q := range unnamed {
		raw, err := json.Marshal(q)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"name"`) {
			t.Fatalf("unnamed query JSON contains name: %s", raw)
		}
	}
}

// Names ride through alias merge searches, including an alias of aliases,
// and remain deterministic regardless of concurrent child execution.
func TestNamedQueryAliasMerge(t *testing.T) {
	newChild := func(docs map[string]map[string]interface{}) Index {
		idx, err := NewMemOnly(NewIndexMapping())
		if err != nil {
			t.Fatal(err)
		}
		batch := idx.NewBatch()
		for id, doc := range docs {
			if err := batch.Index(id, doc); err != nil {
				t.Fatal(err)
			}
		}
		if err := idx.Batch(batch); err != nil {
			t.Fatal(err)
		}
		return idx
	}
	idx1 := newChild(map[string]map[string]interface{}{
		"1": {"desc": "red fish"}, "2": {"desc": "blue fish"},
	})
	idx2 := newChild(map[string]map[string]interface{}{
		"3": {"desc": "red bird"}, "4": {"desc": "green grass"},
	})
	defer func() {
		_ = idx1.Close()
		_ = idx2.Close()
	}()
	alias := NewIndexAlias(idx1, idx2)

	disj := query.NewDisjunctionQuery([]query.Query{
		namedTerm("red", "desc", "rule-red"),
		namedTerm("fish", "desc", "rule-fish"),
	})
	disj.SetMin(1)

	expected := map[int64][]string{
		1: {"rule-fish", "rule-red"},
		2: {"rule-fish"},
		3: {"rule-red"},
	}
	for iter := 0; iter < 20; iter++ {
		req := NewSearchRequest(disj)
		req.Size = 10
		res, err := alias.Search(req)
		if err != nil {
			t.Fatal(err)
		}
		names := hitNamesMap(t, res)
		if !reflect.DeepEqual(names, expected) {
			t.Fatalf("iter %d: %v", iter, names)
		}
	}

	// alias of aliases reports names too
	top := NewIndexAlias(NewIndexAlias(idx1), NewIndexAlias(idx2))
	req := NewSearchRequest(namedTerm("red", "desc", "rule-red"))
	req.Size = 10
	res, err := top.Search(req)
	if err != nil {
		t.Fatal(err)
	}
	names := hitNamesMap(t, res)
	if len(names) != 2 || !reflect.DeepEqual(names[1], []string{"rule-red"}) ||
		!reflect.DeepEqual(names[3], []string{"rule-red"}) {
		t.Fatalf("alias of aliases: %v", names)
	}
}

// Recycled DocumentMatch objects must not carry names across searches or
// hits (pool safety), especially after exclusion branches stamped names.
func TestNamedQueryPoolDoesNotLeakNames(t *testing.T) {
	idx := newNamedTestIndex(t)
	defer func() { _ = idx.Close() }()

	excluding := query.NewBooleanQuery(
		[]query.Query{namedTerm("red", "desc", "must-red")},
		nil,
		[]query.Query{namedTerm("bird", "desc", "mustnot-bird")},
	)
	for i := 0; i < 5; i++ {
		res, err := idx.Search(NewSearchRequest(excluding))
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range res.Hits {
			for _, n := range hit.HitNames {
				if n == "mustnot-bird" {
					t.Fatalf("must_not name leaked: %v", hit.HitNames)
				}
			}
		}
	}

	// a subsequent unrelated named search must not see stale names
	q := namedTerm("fish", "desc", "fish-only")
	res, err := idx.Search(NewSearchRequest(q))
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range res.Hits {
		if !reflect.DeepEqual(hit.HitNames, []string{"fish-only"}) {
			t.Fatalf("stale names observed: %v", hit.HitNames)
		}
	}
}

// Unnamed leaf searches keep HitNames nil all the way down, i.e. the
// feature pays no cost and changes no JSON when unused.
func TestNamedQueryUnnamedHasNoNames(t *testing.T) {
	idx := newNamedTestIndex(t)
	defer func() { _ = idx.Close() }()

	bq := query.NewBooleanQuery(
		[]query.Query{query.NewTermQuery("red")},
		[]query.Query{query.NewTermQuery("fish")},
		nil,
	)
	req := NewSearchRequest(bq)
	req.Size = 10
	res, err := idx.Search(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected hits")
	}
	for _, hit := range res.Hits {
		if hit.HitNames != nil {
			t.Fatalf("unnamed query produced names: %v", hit.HitNames)
		}
	}
}

// A named MatchNone clause stays neutral (same behavior as an unnamed
// one): its name can never appear, and boolean neutrality handling is not
// defeated by the wrapper.
func TestNamedQueryMatchNoneNeutrality(t *testing.T) {
	idx := newNamedTestIndex(t)
	defer func() { _ = idx.Close() }()

	mn := query.NewMatchNoneQuery()
	mn.SetName("never-matches")
	bq := query.NewBooleanQuery(nil,
		[]query.Query{namedTerm("fish", "desc", "fish-rule")}, nil)
	// a match-none clause assigned directly as the must branch is treated
	// as a neutral empty must by the boolean query, named or not
	bq.Must = mn
	req := NewSearchRequest(bq)
	req.Size = 10
	res, err := idx.Search(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected hits from should with neutral must, got none")
	}
	for _, hit := range res.Hits {
		for _, n := range hit.HitNames {
			if n == "never-matches" {
				t.Fatalf("match_none name surfaced on %s: %v", hit.ID, hit.HitNames)
			}
		}
	}
	if got := hitNamesMap(t, res)[2]; !reflect.DeepEqual(got, []string{"fish-rule"}) {
		t.Fatalf("doc 2: %v", got)
	}
}

// direct semantics of the sorted, de-duplicated hit-name set
func TestDocumentMatchHitNameSet(t *testing.T) {
	dm := search.DocumentMatch{}
	dm.AddHitName("b")
	dm.AddHitName("a")
	dm.AddHitName("b")
	dm.AddHitName("")
	if !reflect.DeepEqual(dm.HitNames, []string{"a", "b"}) {
		t.Fatalf("set: %v", dm.HitNames)
	}
	other := &search.DocumentMatch{}
	other.AddHitName("c")
	other.AddHitName("a")
	dm.MergeHitNamesFrom(other, nil)
	if !reflect.DeepEqual(dm.HitNames, []string{"a", "b", "c"}) {
		t.Fatalf("merged set: %v", dm.HitNames)
	}
	dm.Reset()
	if len(dm.HitNames) != 0 {
		t.Fatalf("reset failed: %v", dm.HitNames)
	}
	// backing array is reused and visible entries stay empty
	dm.AddHitName("x")
	if !reflect.DeepEqual(dm.HitNames, []string{"x"}) {
		t.Fatalf("reuse failed: %v", dm.HitNames)
	}
}
