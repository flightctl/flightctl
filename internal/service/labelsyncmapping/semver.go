package labelsyncmapping

import (
	"fmt"
	"reflect"
	"strings"
	"unicode"

	semver "github.com/coreos/go-semver/semver"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

var semverCELType = cel.ObjectType("flightctl.Semver")

type semverValue struct {
	semver.Version
}

func (v semverValue) ConvertToNative(typeDesc reflect.Type) (any, error) {
	switch typeDesc {
	case reflect.TypeFor[semver.Version]():
		return v.Version, nil
	case reflect.TypeFor[string]():
		return v.Version.String(), nil
	default:
		return nil, fmt.Errorf("type conversion error from %q to %q", semverCELType.TypeName(), typeDesc)
	}
}

func (v semverValue) ConvertToType(typeVal ref.Type) ref.Val {
	switch typeVal {
	case semverCELType:
		return v
	case types.TypeType:
		return semverCELType
	default:
		return types.NewErr("type conversion error from %q to %q", semverCELType.TypeName(), typeVal)
	}
}

func (v semverValue) Equal(other ref.Val) ref.Val {
	otherVersion, ok := other.(semverValue)
	if !ok {
		return types.MaybeNoSuchOverloadErr(other)
	}
	return types.Bool(v.Version.Equal(otherVersion.Version))
}

func (v semverValue) Type() ref.Type {
	return semverCELType
}

func (v semverValue) Value() any {
	return v.Version
}

type semverLibraryType struct{}

func semverLibrary() cel.EnvOption {
	return cel.Lib(semverLibraryType{})
}

func (semverLibraryType) LibraryName() string {
	return "flightctl.Semver"
}

func (semverLibraryType) Types() []*cel.Type {
	return []*cel.Type{semverCELType}
}

func (semverLibraryType) CompileOptions() []cel.EnvOption {
	return []cel.EnvOption{
		cel.Function("semver",
			cel.Overload("string_to_semver", []*cel.Type{cel.StringType}, semverCELType, cel.UnaryBinding(stringToSemver)),
		),
		cel.Function("isSemver",
			cel.Overload("is_semver_string", []*cel.Type{cel.StringType}, cel.BoolType, cel.UnaryBinding(isSemver)),
		),
		cel.Function("compareTo",
			cel.MemberOverload("semver_compare_to", []*cel.Type{semverCELType, semverCELType}, cel.IntType, cel.BinaryBinding(compareSemver)),
		),
	}
}

func (semverLibraryType) ProgramOptions() []cel.ProgramOption {
	return nil
}

func stringToSemver(value ref.Val) ref.Val {
	version, err := parseSemver(value)
	if err != nil {
		return types.WrapErr(err)
	}
	return semverValue{Version: version}
}

func isSemver(value ref.Val) ref.Val {
	_, err := parseSemver(value)
	return types.Bool(err == nil)
}

func parseSemver(value ref.Val) (semver.Version, error) {
	text, ok := value.Value().(string)
	if !ok {
		return semver.Version{}, fmt.Errorf("semantic version must be a string")
	}
	version, err := semver.NewVersion(strings.TrimLeftFunc(strings.TrimSpace(text), func(r rune) bool {
		return !unicode.IsDigit(r)
	}))
	if err != nil {
		return semver.Version{}, err
	}
	return *version, nil
}

func compareSemver(value, other ref.Val) ref.Val {
	version, ok := value.(semverValue)
	if !ok {
		return types.MaybeNoSuchOverloadErr(value)
	}
	otherVersion, ok := other.(semverValue)
	if !ok {
		return types.MaybeNoSuchOverloadErr(other)
	}
	return types.Int(version.Compare(otherVersion.Version))
}
