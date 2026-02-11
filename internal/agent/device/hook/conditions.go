package hook

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
)

func checkCondition(cond *v1beta1.HookCondition, actionContext *actionContext) (bool, error) {
	if cond == nil {
		return true, nil
	}

	conditionType, err := cond.Type()
	if err != nil {
		return false, err
	}

	switch conditionType {
	case v1beta1.HookConditionTypeExpression:
		expression, err := (*cond).AsHookConditionExpression()
		if err != nil {
			return false, err
		}
		return checkExpressionCondition(expression, actionContext), nil
	case v1beta1.HookConditionTypePathOp:
		pathOp, err := (*cond).AsHookConditionPathOp()
		if err != nil {
			return false, err
		}
		return checkPathOpCondition(pathOp, actionContext), nil
	default:
		return false, fmt.Errorf("%w: %q", errors.ErrUnknownHookConditionType, conditionType)
	}
}

const (
	hookExpressionConditionFmt string = `(\w+)\s*(==)\s*(\w+)`
)

var hookExpressionConditionRegex = regexp.MustCompile(hookExpressionConditionFmt)

func checkExpressionCondition(cond v1beta1.HookConditionExpression, actionCtx *actionContext) bool {
	match := hookExpressionConditionRegex.FindStringSubmatch(cond)
	if match == nil {
		return false
	}
	lhs, op, rhs := match[1], match[2], match[3]
	switch lhs {
	case "rebooted":
		b, err := strconv.ParseBool(rhs)
		if err != nil {
			return false
		}
		return checkOp(actionCtx.systemRebooted, op, b)
	default:
		return false
	}
}

func checkOp[T comparable](lhs T, op string, rhs T) bool {
	switch op {
	case "==":
		return checkEquals(lhs, rhs)
	default:
		return false
	}
}

func checkEquals[T comparable](a, b T) bool {
	return a == b
}

func checkPathOpCondition(cond v1beta1.HookConditionPathOp, actionCtx *actionContext) bool {
	resetCommandLineVars(actionCtx)

	isPathToDir := len(cond.Path) > 0 && cond.Path[len(cond.Path)-1] == '/'
	if isPathToDir {
		return checkPathOpConditionForDir(cond, actionCtx)
	}
	return checkPathOpConditionForFile(cond, actionCtx)
}

// checkFileOpConditionForDir checks whether a specified operation (create, update, remove) has been performed
// on any file in the tree rooted at the specified path.
// As a side-effect, it populates the command line variables of the action context with the corresponding list of files.
func checkPathOpConditionForDir(cond v1beta1.HookConditionPathOp, actionCtx *actionContext) bool {
	dirPath := ensureTrailingSlash(cond.Path)
	conditionMet := false
	if slices.Contains(cond.Op, v1beta1.FileOperationCreated) {
		if treeFromPathContains(dirPath, actionCtx.createdFiles) {
			files := getContainedFiles(dirPath, actionCtx.createdFiles)
			appendFiles(actionCtx, FilesKey, files...)
			appendFiles(actionCtx, CreatedKey, files...)
			conditionMet = true
		}
	}
	if slices.Contains(cond.Op, v1beta1.FileOperationUpdated) {
		if treeFromPathContains(dirPath, actionCtx.updatedFiles) {
			files := getContainedFiles(dirPath, actionCtx.updatedFiles)
			appendFiles(actionCtx, FilesKey, files...)
			appendFiles(actionCtx, UpdatedKey, files...)
			conditionMet = true
		}
	}
	if slices.Contains(cond.Op, v1beta1.FileOperationRemoved) {
		if treeFromPathContains(dirPath, actionCtx.removedFiles) {
			files := getContainedFiles(dirPath, actionCtx.removedFiles)
			appendFiles(actionCtx, FilesKey, files...)
			appendFiles(actionCtx, RemovedKey, files...)
			conditionMet = true
		}
	}
	if conditionMet {
		actionCtx.commandLineVars[PathKey] = dirPath
	}
	return conditionMet
}

// checkFileOpConditionForFile checks whether a specified operation (create, update, remove) has been performed
// on the specified file.
// As a side-effect, it populates the command line variables of the action context with the corresponding list of files.
func checkPathOpConditionForFile(cond v1beta1.HookConditionPathOp, actionCtx *actionContext) bool {
	conditionMet := false
	if slices.Contains(cond.Op, v1beta1.FileOperationCreated) {
		if pathEquals(cond.Path, actionCtx.createdFiles) {
			appendFiles(actionCtx, FilesKey, cond.Path)
			appendFiles(actionCtx, CreatedKey, cond.Path)
			conditionMet = true
		}
	}
	if slices.Contains(cond.Op, v1beta1.FileOperationUpdated) {
		if pathEquals(cond.Path, actionCtx.updatedFiles) {
			appendFiles(actionCtx, FilesKey, cond.Path)
			appendFiles(actionCtx, UpdatedKey, cond.Path)
			conditionMet = true
		}
	}
	if slices.Contains(cond.Op, v1beta1.FileOperationRemoved) {
		if pathEquals(cond.Path, actionCtx.removedFiles) {
			appendFiles(actionCtx, FilesKey, cond.Path)
			appendFiles(actionCtx, RemovedKey, cond.Path)
			conditionMet = true
		}
	}
	if conditionMet {
		actionCtx.commandLineVars[PathKey] = cond.Path
	}
	return conditionMet
}

func ensureTrailingSlash(path string) string {
	if len(path) < 1 || path[len(path)-1] != '/' {
		return path + "/"
	}
	return path
}

func pathEquals(path string, files map[string]v1beta1.FileSpec) bool {
	_, ok := files[path]
	return ok
}

func treeFromPathContains(path string, files map[string]v1beta1.FileSpec) bool {
	// ensure path ends with a trailing '/', so HasPrefix() doesn't accidentally match a file with a similar prefix
	path = ensureTrailingSlash(path)
	for file := range files {
		if strings.HasPrefix(file, path) {
			return true
		}
	}
	return false
}

func getContainedFiles(path string, files map[string]v1beta1.FileSpec) []string {
	containedFiles := []string{}
	for file := range files {
		if strings.HasPrefix(file, path) {
			containedFiles = append(containedFiles, file)
		}
	}
	return containedFiles
}

func appendFiles(actionCtx *actionContext, key CommandLineVarKey, files ...string) {
	if len(actionCtx.commandLineVars[key]) > 0 {
		actionCtx.commandLineVars[key] = strings.Join(append([]string{actionCtx.commandLineVars[key]}, files...), " ")
	} else {
		actionCtx.commandLineVars[key] = strings.Join(files, " ")
	}
}
