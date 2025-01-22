/*
Copyright 2014 The Kubernetes Authors.

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

package labels

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/pkg/k8s/selector"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Parse takes a string representing a selector and returns a selector
// object, or panic when an error occur. This parsing function differs from ParseSelector
// as they parse different selectors with different syntaxes.
// The input will cause an error if it does not follow this form:
//
//	<selector-syntax>         ::= <requirement> | <requirement> "," <selector-syntax>
//	<requirement>             ::= [!] KEY [ <set-based-restriction> | <exact-match-restriction> ]
//	<set-based-restriction>   ::= "" | <inclusion-exclusion> <value-set>
//	<inclusion-exclusion>     ::= <inclusion> | <exclusion>
//	<exclusion>               ::= "notin"
//	<inclusion>               ::= "in"
//	<value-set>               ::= "(" <values> ")"
//	<values>                  ::= VALUE | VALUE "," <values>
//	<exact-match-restriction> ::= ["="|"=="|"!="] VALUE
//
// KEY is a sequence of one or more characters following [ DNS_SUBDOMAIN "/" ] DNS_LABEL. Max length is 63 characters.
// VALUE is a sequence of zero or more characters "([A-Za-z0-9_-\.])". Max length is 63 characters.
// Delimiter is white space: (' ', '\t')
// Example of valid syntax:
//
//	"x in (foo,,baz),y,z notin ()"
//
// Note:
//  1. Inclusion - " in " - denotes that the KEY exists and is equal to any of the
//     VALUEs in its requirement
//  2. Exclusion - " notin " - denotes that the KEY is not equal to any
//     of the VALUEs in its requirement or does not exist
//  3. The empty string is a valid VALUE
//  4. A requirement with just a KEY - as in "y" above - denotes that
//     the KEY exists and can be any VALUE.
//  5. A requirement with just !KEY requires that the KEY not exist.
func ParseSelectorOrDie(s string) selector.Selector {
	parsedSelector, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return parsedSelector
}

// Parse takes a string representing a selector and returns a selector
// object, or an error. This parsing function differs from ParseSelector
// as they parse different selectors with different syntaxes.
// The input will cause an error if it does not follow this form:
//
//	<selector-syntax>         ::= <requirement> | <requirement> "," <selector-syntax>
//	<requirement>             ::= [!] KEY [ <set-based-restriction> | <exact-match-restriction> ]
//	<set-based-restriction>   ::= "" | <inclusion-exclusion> <value-set>
//	<inclusion-exclusion>     ::= <inclusion> | <exclusion>
//	<exclusion>               ::= "notin"
//	<inclusion>               ::= "in"
//	<value-set>               ::= "(" <values> ")"
//	<values>                  ::= VALUE | VALUE "," <values>
//	<exact-match-restriction> ::= ["="|"=="|"!="] VALUE
//
// KEY is a sequence of one or more characters following [ DNS_SUBDOMAIN "/" ] DNS_LABEL. Max length is 63 characters.
// VALUE is a sequence of zero or more characters "([A-Za-z0-9_-\.])". Max length is 63 characters.
// Delimiter is white space: (' ', '\t')
// Example of valid syntax:
//
//	"x in (foo,,baz),y,z notin ()"
//
// Note:
//  1. Inclusion - " in " - denotes that the KEY exists and is equal to any of the
//     VALUEs in its requirement
//  2. Exclusion - " notin " - denotes that the KEY is not equal to any
//     of the VALUEs in its requirement or does not exist
//  3. The empty string is a valid VALUE
//  4. A requirement with just a KEY - as in "y" above - denotes that
//     the KEY exists and can be any VALUE.
//  5. A requirement with just !KEY requires that the KEY not exist.
func Parse(s string, opts ...field.PathOption) (selector.Selector, error) {
	parsedSelector, err := selector.Parse(s, opts...)
	if err != nil {
		return nil, err
	}

	requirements, _ := parsedSelector.Requirements()
	if err := validate(requirements, field.ToPath(opts...)); err != nil {
		return nil, err
	}
	return parsedSelector, nil
}

// ParseToRequirements takes a string representing a selector and returns a list of
// requirements. This function is suitable for those callers that perform additional
// processing on selector requirements.
func ParseToRequirements(selector string, opts ...field.PathOption) ([]selector.Requirement, error) {
	parsedSelector, err := Parse(selector, opts...)
	if err != nil {
		return nil, err
	}

	requirements, _ := parsedSelector.Requirements()
	return requirements, nil
}

func validate(requirements []selector.Requirement, path *field.Path) error {
	var allErrs field.ErrorList
	for _, r := range requirements {
		key := r.Key()       // A set of keys (RequirementKey)
		values := r.Values() // A list of corresponding value slices

		// Validate each key in the set
		for i := range key {
			if errs := validation.IsQualifiedName(key[i]); len(errs) != 0 {
				allErrs = append(allErrs, field.Invalid(path.Child("key"), key[i], strings.Join(errs, "; ")))
			}

			p := path.Child("values")
			for j, val := range values {
				// Check for length mismatch between keys and values
				if len(val) != len(key) {
					allErrs = append(allErrs, field.Invalid(
						p.Index(j).Key(key.String()), val,
						fmt.Sprintf("length mismatch: key set %v has %d elements but value %v has %d elements",
							key, len(key), val, len(val)),
					))
					continue
				}

				// Validate the value corresponding to the i-th key
				if errs := validation.IsValidLabelValue(val[i]); len(errs) != 0 {
					allErrs = append(allErrs, field.Invalid(p.Index(j).Key(key.String()), val[i], strings.Join(errs, "; ")))
				}
			}
		}
	}
	return allErrs.ToAggregate()
}
