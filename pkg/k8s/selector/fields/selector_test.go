package fields

import (
	"strings"
	"testing"
)

func TestSelectorParse(t *testing.T) {
	testGoodStrings := []string{
		"x=a,y=b,z=c",
		"",
		"x!=a,y=b",
		"x=",
		"x= ",
		"x=,z= ",
		"x= ,z= ",
		"!x",
		"x>1",
		"x>1,z<5",
		"x>=1",
		"x>=1,z<=5",
		"x>=1",
		"x>=1,z<=5",
		"x>=2024-10-24T10:00:00Z",
		"x=a||y=b",
		"x==a==b",
		"x=msg:\\(hello!world\\)",
		"x,x=a",
		"!x,x!=a",
		"x=,y=",
		"!x,!y",
		"x,y=",
		"!x,y=",
		"",
	}
	testBadStrings := []string{
		",",
		"!x=a",
		"x<a",
		"x<2024-10-24T10:00:00",
	}
	for _, test := range testGoodStrings {
		lq, err := ParseSelector(test)
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", test, err, err)
			continue
		}
		s := strings.Replace(test, " ", "", -1)
		s = strings.Replace(s, "\\", "", -1)
		if s != lq.String() {
			t.Errorf("%v restring gave: %v\n", test, lq.String())
		}
	}
	for _, test := range testBadStrings {
		_, err := ParseSelector(test)
		if err == nil {
			t.Errorf("%v: did not get expected error\n", test)
		}
	}
}
