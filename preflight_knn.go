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
	"fmt"

	"github.com/blevesearch/bleve/v2/document"
	"github.com/blevesearch/bleve/v2/search/query"
)

func init() {
	vectorClauseCapability = func(q query.Query) (string, FieldCapability, int) {
		if knn, ok := q.(*query.KNNQuery); ok {
			return "knn", CapabilityVector, len(knn.Vector)
		}
		return "", 0, 0
	}
}

// appendKNNClauseRequirements appends the vector field requirements of the
// request's kNN clauses together with the requirements of their filter
// queries.
func appendKNNClauseRequirements(req *SearchRequest, requirements []*ClauseRequirement) ([]*ClauseRequirement, error) {
	for i, knn := range req.KNN {
		if knn == nil {
			return nil, fmt.Errorf("knn query cannot be nil")
		}
		vector := knn.Vector
		if len(vector) == 0 && knn.VectorBase64 != "" {
			decoded, err := document.DecodeVector(knn.VectorBase64)
			if err != nil {
				return nil, err
			}
			vector = decoded
		}
		path := fmt.Sprintf("knn[%d]", i)
		requirements = append(requirements, &ClauseRequirement{
			Path:       path,
			Clause:     "knn",
			Field:      knn.Field,
			Needs:      CapabilityVector,
			VectorDims: len(vector),
		})
		if knn.FilterQuery != nil {
			var err error
			requirements, err = extractClauseRequirements(knn.FilterQuery, path+".filter", requirements)
			if err != nil {
				return nil, err
			}
		}
	}
	return requirements, nil
}
