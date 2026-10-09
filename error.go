//  Copyright (c) 2014 Couchbase, Inc.
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

import "fmt"

// Constant Error values which can be compared to determine the type of error
const (
	ErrorIndexPathExists Error = iota
	ErrorIndexPathDoesNotExist
	ErrorIndexMetaMissing
	ErrorIndexMetaCorrupt
	ErrorIndexClosed
	ErrorAliasMulti
	ErrorAliasEmpty
	ErrorUnknownIndexType
	ErrorEmptyID
	ErrorIndexReadInconsistency
	ErrorTwoPhaseSearchInconsistency
	ErrorSynonymSearchNotSupported
	ErrorTrainingNotSupported
	ErrorVersionConflict
)

// VersionConflictError describes a single version-conditional mutation
// which could not be applied because the document's current version no
// longer matched the expected version.
type VersionConflictError struct {
	DocID    string
	Expected uint64
	Current  uint64
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("version conflict for document %q: expected version %d, current version %d",
		e.DocID, e.Expected, e.Current)
}

func (e *VersionConflictError) Is(target error) bool {
	t, ok := target.(Error)
	return ok && t == ErrorVersionConflict
}

// VersionConflictErrors is returned when a version-conditional batch
// contains one or more documents whose expected versions did not match.
// The batch is atomic, so none of its operations are applied.
type VersionConflictErrors []*VersionConflictError

func (errs VersionConflictErrors) Error() string {
	return fmt.Sprintf("version conflicts on %d document(s)", len(errs))
}

func (errs VersionConflictErrors) Is(target error) bool {
	t, ok := target.(Error)
	return ok && t == ErrorVersionConflict
}

// Error represents a more strongly typed bleve error for detecting
// and handling specific types of errors.
type Error int

func (e Error) Error() string {
	return errorMessages[e]
}

var errorMessages = map[Error]string{
	ErrorIndexPathExists:             "cannot create new index, path already exists",
	ErrorIndexPathDoesNotExist:       "cannot open index, path does not exist",
	ErrorIndexMetaMissing:            "cannot open index, metadata missing",
	ErrorIndexMetaCorrupt:            "cannot open index, metadata corrupt",
	ErrorIndexClosed:                 "index is closed",
	ErrorAliasMulti:                  "cannot perform single index operation on multiple index alias",
	ErrorAliasEmpty:                  "cannot perform operation on empty alias",
	ErrorUnknownIndexType:            "unknown index type",
	ErrorEmptyID:                     "document ID cannot be empty",
	ErrorIndexReadInconsistency:      "index read inconsistency detected",
	ErrorTwoPhaseSearchInconsistency: "2-phase search failed, likely due to an overlapping topology change",
	ErrorSynonymSearchNotSupported:   "synonym search not supported",
	ErrorVersionConflict:             "document version conflict",
}
