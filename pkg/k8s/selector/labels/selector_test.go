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
	"strings"
	"testing"
)

// TestInvalidLabelSelectors checks that specific label restrictions are enforced.
// These tests were moved from the generic selector's tests after label restrictions were removed there
// and are now validated in this test for label-specific restrictions.
func TestInvalidLabelSelectors(t *testing.T) {
	testBadSelectors := []string{
		"key=axahm2EJ8Phiephe2eixohbee9eGeiyees1thuozi1xoh0GiuH3diewi8iem7Nui", //value too long
		strings.Repeat("a", 254), //breaks DNS rule that len(key) <= 253
		"key=" + strings.Repeat("a", 254),
		"key=a\\ b",
	}

	for _, test := range testBadSelectors {
		_, err := Parse(test)
		if err == nil {
			t.Errorf("%v: did not get expected error\n", test)
		}
	}
}
