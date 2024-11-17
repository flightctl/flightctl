/*
Copyright 2015 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Modifications by Assaf Albo (asafbss): Added support for the containment operator and unified fields and labels selector.
*/

package fields

import (
	"github.com/flightctl/flightctl/pkg/k8s/selector"
)

// ParseSelectorOrDie takes a string representing a selector and returns an
// object suitable for matching, or panic when an error occur.
func ParseSelectorOrDie(s string) selector.Selector {
	parsedSelector, err := ParseSelector(s)
	if err != nil {
		panic(err)
	}
	return parsedSelector
}

// ParseSelector takes a string representing a selector and returns an
// object suitable for matching, or an error.
func ParseSelector(s string) (selector.Selector, error) {
	return selector.ParseWithLexer(s, &lexer{s: s, pos: 0})
}
