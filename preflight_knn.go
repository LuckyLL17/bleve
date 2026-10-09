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

//go:build vectors
// +build vectors

package bleve

import (
	"fmt"

	"github.com/blevesearch/bleve/v2/document"
	"github.com/blevesearch/bleve/v2/search/query"
	index "github.com/blevesearch/bleve_index_api"
)

func (c *requestCompiler) compileKNN() {
	r := c.req
	for i, knn := range r.KNN {
		clause := fmt.Sprintf("knn[%d]", i)
		if knn == nil {
			c.add(true, PreflightSectionKNN, "", clause, "knn query cannot be nil")
			continue
		}
		if len(knn.Vector) == 0 && knn.VectorBase64 != "" {
			decoded, err := document.DecodeVector(knn.VectorBase64)
			if err != nil {
				c.add(true, PreflightSectionKNN, knn.Field, clause, err.Error())
				continue
			}
			knn.Vector = decoded
		}
		if knn.K <= 0 || len(knn.Vector) == 0 {
			c.add(true, PreflightSectionKNN, knn.Field, clause,
				"k must be greater than 0 and vector must be non-empty")
		}
		if knn.K > BleveMaxK {
			c.add(true, PreflightSectionKNN, knn.Field, clause,
				fmt.Sprintf("k must be less than %d", BleveMaxK))
		}
		if knn.Field == "" {
			c.add(true, PreflightSectionKNN, "", clause,
				"knn query field must be non-empty")
		}
		if c.m == nil {
			if vfq, ok := knn.FilterQuery.(query.ValidatableQuery); ok {
				if err := vfq.Validate(); err != nil {
					c.add(true, PreflightSectionKNN, knn.Field, clause,
						fmt.Sprintf("knn filter query is invalid: %v", err))
				}
			}
		} else if knn.FilterQuery != nil {
			c.compileKNNFilter(knn.FilterQuery, knn.Field, clause)
		}
		if c.m == nil || knn.Field == "" {
			continue
		}
		fm, mapped := c.resolveField(knn.Field)
		if !mapped {
			c.add(true, PreflightSectionKNN, knn.Field, clause,
				fmt.Sprintf("knn field '%s' is not mapped", knn.Field))
			continue
		}
		if fm.Type == "" {
			continue
		}
		if fm.Type != "vector" && fm.Type != "vector_base64" {
			c.add(true, PreflightSectionKNN, knn.Field, clause,
				fmt.Sprintf("knn requires field '%s' to be a vector field, mapped as '%s'",
					knn.Field, fm.Type))
			continue
		}
		if fm.Dims != 0 && len(knn.Vector) != 0 && fm.Dims != len(knn.Vector) {
			c.add(true, PreflightSectionKNN, knn.Field, clause,
				fmt.Sprintf("knn vector has %d dimensions, field '%s' indexes %d dimensions",
					len(knn.Vector), knn.Field, fm.Dims))
		}
		if c.reader != nil {
			if _, ok := c.reader.(index.VectorIndexReader); !ok {
				c.add(true, PreflightSectionKNN, knn.Field, clause,
					fmt.Sprintf("index does not support vector search on field '%s'",
						knn.Field))
			}
		}
	}
	switch r.KNNOperator {
	case knnOperatorAnd, knnOperatorOr, "":
	default:
		c.add(true, PreflightSectionKNN, "", "",
			"knn_operator must be either 'and' / 'or'")
	}
}

// compileKNNFilter runs the same query diagnostics on the KNN pre-filter
// query and attributes them to the enclosing KNN clause.
func (c *requestCompiler) compileKNNFilter(filterQuery query.Query,
	field, clause string) {
	child := newRequestCompiler(&SearchRequest{Query: filterQuery}, c.m, c.reader)
	child.strict = true
	child.compileQuery()
	for _, issue := range child.issues {
		issue.Section = PreflightSectionKNN
		issue.Name = clause
		if issue.Field == "" {
			issue.Field = field
		}
		c.issues = append(c.issues, issue)
	}
}
