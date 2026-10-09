//  Copyright (c) 2024 Couchbase, Inc.
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

//go:build vectors
// +build vectors

package bleve

import (
	"testing"

	"github.com/blevesearch/bleve/v2/mapping"
)

func TestPreflightKNNVectorDims(t *testing.T) {
	vec3 := mapping.NewVectorFieldMapping()
	vec3.Dims = 3
	idx := newPreflightTestIndex(t, "idx", map[string]*mapping.FieldMapping{
		"vec":  vec3,
		"body": preflightFullTextField(),
	}, "")
	defer func() { _ = idx.Close() }()

	// query vector dimensionality differs from the indexed vector field
	req := NewSearchRequest(NewMatchNoneQuery())
	req.AddKNN("vec", []float32{1, 2, 3, 4}, 5, 1.0)
	report, err := PreflightSearch(idx, req)
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if report.Executable {
		t.Errorf("knn with mismatched dims must not be executable")
	}
	preflightFindProblem(t, report, "idx", "knn[0]", PreflightReasonVectorDimMismatch)

	// matching dimensionality is executable
	reqOK := NewSearchRequest(NewMatchNoneQuery())
	reqOK.AddKNN("vec", []float32{1, 2, 3}, 5, 1.0)
	report, err = PreflightSearch(idx, reqOK)
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if !report.Executable {
		t.Errorf("knn with matching dims must be executable, got %v", report.Indexes["idx"].Problems)
	}

	// kNN targeting a non vector field is rejected
	reqPlain := NewSearchRequest(NewMatchNoneQuery())
	reqPlain.AddKNN("body", []float32{1, 2, 3}, 5, 1.0)
	report, err = PreflightSearch(idx, reqPlain)
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if report.Executable {
		t.Errorf("knn on a text field must not be executable")
	}
	preflightFindProblem(t, report, "idx", "knn[0]", PreflightReasonNotVectorField)

	// kNN filter queries are checked as ordinary clauses
	reqFilter := NewSearchRequest(NewMatchNoneQuery())
	filter := NewTermQuery("x")
	filter.SetField("missing")
	reqFilter.AddKNNWithFilter("vec", []float32{1, 2, 3}, 5, 1.0, filter)
	report, err = PreflightSearch(idx, reqFilter)
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if report.Executable {
		t.Errorf("knn with incompatible filter must not be executable")
	}
	preflightFindProblem(t, report, "idx", "knn[0].filter", PreflightReasonFieldNotMapped)
}
