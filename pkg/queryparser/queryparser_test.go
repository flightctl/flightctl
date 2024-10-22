package queryparser

import (
	"context"
	"testing"
)

func TestTokenizerSyntax(t *testing.T) {
	ctx := context.Background()

	testGoodSyntax := map[string]TokenSet{
		"":             {},
		"[]":           NewTokenSet().AddValueToken("[]"),
		"some text":    NewTokenSet().AddValueToken("some text"),
		"some\\ text":  NewTokenSet().AddValueToken("some text"),
		"some, text":   NewTokenSet().AddValueToken("some").AddValueToken("text"),
		"some\\, text": NewTokenSet().AddValueToken("some, text"),
		"func()":       NewTokenSet().AddFunctionToken("func", nil),
		"func(I want '\\(' symbol)": NewTokenSet().AddFunctionToken("func", func() TokenSet {
			return NewTokenSet().AddValueToken("I want '(' symbol")
		}),
		"func1(),func2(test2)": NewTokenSet().AddFunctionToken("func1", nil).
			AddFunctionToken("func2", func() TokenSet {
				return NewTokenSet().AddValueToken("test2")
			}),
		"func1(),text,func2(test2)": NewTokenSet().AddFunctionToken("func1", nil).
			AddValueToken("text").AddFunctionToken("func2", func() TokenSet {
			return NewTokenSet().AddValueToken("test2")
		}),
		"func(func())": NewTokenSet().AddFunctionToken("func", func() TokenSet {
			return NewTokenSet().AddFunctionToken("func", nil)
		}),
		"func(func(some text \\(i.e.\\, something\\)))": NewTokenSet().AddFunctionToken("func", func() TokenSet {
			return NewTokenSet().AddFunctionToken("func", func() TokenSet {
				return NewTokenSet().AddValueToken("some text (i.e., something)")
			})
		}),
	}

	for test, expectedSet := range testGoodSyntax {
		tokens, err := Tokenize(ctx, test)
		if err != nil {
			t.Errorf("Error tokenizing %q: %v", test, err)
			continue
		}

		if !tokens.Matches(expectedSet) {
			t.Errorf("%q: tokens do not match. Expected: %v, Got: %v", test, expectedSet, tokens)
		}
	}

	testBadSyntax := []string{
		"()",
		",test",
		",func()",
		",,func()",
		",test,func()",
		"func(I want '(' symbol)",
		"func\\(test)",
		"(func)",
		"func()!",
		"func(())",
		"func(()",
		"func())",
		"func(),,func()",
		"func()func()",
	}

	for _, test := range testBadSyntax {
		_, err := Tokenize(ctx, test)
		if err == nil {
			t.Errorf("Expected error for invalid test %q, but got none", test)
		}
	}
}

var (
	funcSet = QueryFuncSet{
		"func1": QueryFuncHandler{UsedBy: NewSet[string]().Add(RootFunc, "func2", "func3", "func4")},
		"func2": QueryFuncHandler{UsedBy: NewSet[string]().Add(RootFunc, "func3", "func4")},
		"func3": QueryFuncHandler{UsedBy: NewSet[string]().Add(RootFunc, "func4")},
		"func4": QueryFuncHandler{UsedBy: NewSet[string]().Add(RootFunc)},
	}
	noFuncOrder    []string = []string{}
	oneFuncOrder   []string = []string{"func1"}
	twoFuncOrder   []string = []string{"func1", "func2"}
	threeFuncOrder []string = []string{"func1", "func2", "func3"}
	fourFunc       []string = []string{"func1", "func2", "func3", "func4"}
)

func TestParserSequence(t *testing.T) {
	ctx := context.Background()
	qt := newQueryTester()

	testGoodSequence := map[string][]string{
		"":                                noFuncOrder,
		"func1()":                         oneFuncOrder,
		"func1(),func2()":                 twoFuncOrder,
		"func2(func1())":                  twoFuncOrder,
		"func1(),func2(),func3()":         threeFuncOrder,
		"func2(func1()),func3()":          threeFuncOrder,
		"func1(),func3(func2())":          threeFuncOrder,
		"func3(func1(),func2())":          threeFuncOrder,
		"func1(),func2(),func3(),func4()": fourFunc,
		"func2(func1()),func3(),func4()":  fourFunc,
		"func1(),func3(func2()),func4()":  fourFunc,
		"func1(),func2(),func4(func3())":  fourFunc,
		"func2(func1()),func4(func3())":   fourFunc,
		"func3(func1(),func2()),func4()":  fourFunc,
		"func1(),func4(func2(),func3())":  fourFunc,
		"func1(),func4(func3(func2()))":   fourFunc,
		"func3(func2(func1())),func4()":   fourFunc,
		"func4(func3(func2(func1())))":    fourFunc,
	}

	for test, match := range testGoodSequence {
		res, err := qt.Parse(ctx, funcSet, test)
		if err != nil {
			t.Errorf("Error parsing %v: %v", test, err)
			continue
		}

		if len(res.order) != len(match) {
			t.Errorf("%q: incorrect order. expected %v, Got %v", test, match, res.order)
			continue
		}

		for i, f := range res.order {
			if match[i] != f {
				t.Errorf("%q: incorrect order. Expected %v, Got %v", test, match, res.order)
			}
		}
	}

	// A function can only be nested within a function with a higher number.
	// Undefined functions will result in an error.
	testBadSequence := []string{
		"func1(func2())",
		"func2(func2())",
		"func2(func1()),func3(func4())",
		"func1(func2()),func3(func4())",
		"funcInvalid()",
		"func1(func2()),funcInvalid(func4())",
	}

	for _, test := range testBadSequence {
		_, err := qt.Parse(ctx, funcSet, test)
		if err == nil {
			t.Errorf("Expected error for invalid test %q, but got none", test)
		}
	}
}

func TestParserParams(t *testing.T) {
	ctx := context.Background()
	qt := newQueryTester()

	testGoodParams := map[string]*Set[string]{
		"":                              {},
		"a":                             NewSet[string]().Add("a"),
		"$1":                            NewSet[string]().Add("a"),
		"func1($1)":                     NewSet[string]().Add("a"),
		"func1($2)":                     NewSet[string]().Add("", "a"),
		"func2($1)":                     NewSet[string]().Add(""),
		"func3($1)":                     NewSet[string]().Add("!@#$%^&*()"),
		"func1($1),b":                   NewSet[string]().Add("a", "b"),
		"func1($1),$2":                  NewSet[string]().Add("a", "b"),
		"func1($1, $2, b)":              NewSet[string]().Add("a", "b"),
		"func1($1),func2($2)":           NewSet[string]().Add("$1", "$2"),
		"a,func2($2),func3($3)":         NewSet[string]().Add("a", "b", "c"),
		"func1($1),func2($2),func3($3)": NewSet[string]().Add("a", "b", "c"),
		"func1($1),func2($1),func3($1)": NewSet[string]().Add("a"),
		"func1($1),func2($2),func3($2)": NewSet[string]().Add("a", "b"),
		"func1($1),func3(func2($2))":    NewSet[string]().Add("a", "b"),
		"func1($1),func3($1,func2($2))": NewSet[string]().Add("a", "b"),
		"func3(a,func2(b,func1(c)))":    NewSet[string]().Add("a", "b", "c"),
		"func3(func2(func1(a,b,c)))":    NewSet[string]().Add("a", "b", "c"),
	}

	testBadParams := map[string]*Set[string]{
		"$1":                     {},
		"func1($2)":              NewSet[string]().Add("a"),
		"func1($1,$3)":           NewSet[string]().Add("a"),
		"$2,func2($2),func3($3)": NewSet[string]().Add("a"),
	}

	for test, params := range testGoodParams {
		paramsLst := params.List()
		res, err := qt.Parse(ctx, funcSet, test, paramsLst...)
		if err != nil {
			t.Errorf("Error parsing %v: %v", test, err)
			continue
		}

		for _, param := range res.params {
			if !params.Contains(param) {
				t.Errorf("%q: incorrect param. Expected %v, Got %s", test, paramsLst, param)
			}
		}
	}

	for test, params := range testBadParams {
		paramsLst := params.List()
		_, err := qt.Parse(ctx, funcSet, test, paramsLst...)
		if err == nil {
			t.Errorf("Expected error for invalid test %q, but got none", test)
		}
	}

}

type execResult struct {
	order  []string
	params []string
}

func newExecResult() *execResult {
	return &execResult{
		order:  make([]string, 0),
		params: make([]string, 0),
	}
}

type queryTester struct {
}

func newQueryTester() *queryTester {
	return &queryTester{}
}

func (qt *queryTester) Parse(ctx context.Context, set QueryFuncSet, input string, params ...string) (*execResult, error) {
	testFuncs := make(QueryFuncSet, len(set))
	for f, h := range set {
		testFuncs[f] = QueryFuncHandler{
			Invoke: qt.exec,
			UsedBy: h.UsedBy,
		}
	}

	res, err := Parse(ctx, input, WithFunctions(testFuncs), WithParams(params))
	if err != nil {
		return nil, err
	}

	order, err := qt.execOrder(res)
	if err != nil {
		return nil, err
	}

	return order, nil
}

func (qt *queryTester) exec(qf *QueryFunc) error {
	qf.res = qf.name
	return nil
}

func (qt *queryTester) execOrder(qf *QueryFunc) (*execResult, error) {
	if qf == nil {
		return nil, nil
	}

	res := newExecResult()
	for _, arg := range qf.args {
		if !IsValue(arg) {
			subRes, err := qt.execOrder(arg.(*QueryArgFunc).Value())
			if err != nil {
				return nil, err
			}
			res.order = append(res.order, subRes.order...)
			res.params = append(res.params, subRes.params...)
		} else {
			res.params = append(res.params, arg.(*QueryArgValue).Value().(string))
		}
	}

	if qf.res != nil {
		res.order = append(res.order, qf.res.(string))
	}

	return res, nil
}
