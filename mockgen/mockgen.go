// Copyright 2015 Peter Goetz
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Based on the work done in
// https://github.com/golang/mock/blob/d581abfc04272f381d7a05e4b80163ea4e2b9447/mockgen/mockgen.go

// Package mockgen generates mock implementations of Go interfaces.
package mockgen

import (
	"bytes"
	"fmt"
	"go/format"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/petergtz/pegomock/v4/model"
	"github.com/samber/lo"
)

const mockFrameworkImportPath = "github.com/petergtz/pegomock/v4"

func GenerateOutput(ast *model.Package, source, nameOut, packageOut, selfPackage string) []byte {
	g := generator{}
	g.generateCode(source, ast, nameOut, packageOut, selfPackage)
	return g.formattedOutput()
}

type generator struct {
	buf        bytes.Buffer
	packageMap map[string]string // map from import path to package name
}

func (g *generator) generateCode(source string, pkg *model.Package, structName, pkgName, selfPackage string) {
	g.p("// Code generated by pegomock. DO NOT EDIT.")
	g.p("// Source: %v", source)
	g.emptyLine()

	importPaths := pkg.Imports()
	importPaths[mockFrameworkImportPath] = true
	packageMap, nonVendorPackageMap := generateUniquePackageNamesFor(importPaths)
	g.packageMap = packageMap

	g.p("package %v", pkgName)
	g.emptyLine()
	g.p("import (")
	g.p("\"reflect\"")
	g.p("\"time\"")
	for packagePath, packageName := range nonVendorPackageMap {
		if packagePath != selfPackage && packagePath != "time" && packagePath != "reflect" {
			g.p("%v %q", packageName, packagePath)
		}
	}
	for _, packagePath := range pkg.DotImports {
		g.p(". %q", packagePath)
	}
	g.p(")")

	for _, iface := range pkg.Interfaces {
		sName := structName
		if sName == "" {
			sName = "Mock" + iface.Name
		}
		g.generateMockFor(iface, sName, selfPackage)
	}
}

func generateUniquePackageNamesFor(importPaths map[string]bool) (packageMap, nonVendorPackageMap map[string]string) {
	packageMap = make(map[string]string, len(importPaths))
	nonVendorPackageMap = make(map[string]string, len(importPaths))
	packageNamesAlreadyUsed := make(map[string]bool, len(importPaths))

	sortedImportPaths := lo.Keys(importPaths)
	sort.Strings(sortedImportPaths)
	for _, importPath := range sortedImportPaths {
		sanitizedPackagePathBaseName := sanitize(path.Base(importPath))

		// Local names for an imported package can usually be the basename of the import path.
		// A couple of situations don't permit that, such as duplicate local names
		// (e.g. importing "html/template" and "text/template"), or where the basename is
		// a keyword (e.g. "foo/case").
		// try base0, base1, ...
		packageName := sanitizedPackagePathBaseName
		for i := 0; packageNamesAlreadyUsed[packageName] || token.Lookup(packageName).IsKeyword(); i++ {
			packageName = sanitizedPackagePathBaseName + strconv.Itoa(i)
		}

		// hardcode package name for pegomock, because it's hardcoded in the generated code too
		if importPath == mockFrameworkImportPath {
			packageName = "pegomock"
		}

		packageMap[importPath] = packageName
		packageNamesAlreadyUsed[packageName] = true

		nonVendorPackageMap[vendorCleaned(importPath)] = packageName
	}
	return
}

func vendorCleaned(importPath string) string {
	if split := strings.Split(importPath, "/vendor/"); len(split) > 1 {
		return split[1]
	}
	return importPath
}

// sanitize cleans up a string to make a suitable package name.
// pkgName in reflect mode is the base name of the import path,
// which might have characters that are illegal to have in package names.
func sanitize(s string) string {
	t := ""
	for _, r := range s {
		if t == "" {
			if unicode.IsLetter(r) || r == '_' {
				t += string(r)
				continue
			}
		} else {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
				t += string(r)
				continue
			}
		}
		t += "_"
	}
	if t == "_" {
		t = "x"
	}
	return t
}

func (g *generator) generateMockFor(iface *model.Interface, mockTypeName, selfPackage string) {
	typeParamNames := typeParamsStringFrom(iface.TypeParams, g.packageMap, selfPackage, false)
	typeParams := typeParamsStringFrom(iface.TypeParams, g.packageMap, selfPackage, true)
	g.generateMockType(mockTypeName, typeParams,
		typeParamNames)
	for _, method := range iface.Methods {
		g.generateMockMethod(mockTypeName, typeParamNames, method, selfPackage)
		g.emptyLine()
	}
	g.generateMockVerifyMethods(mockTypeName, typeParamNames)
	g.generateVerifierType(mockTypeName, typeParams, typeParamNames)
	for _, method := range iface.Methods {
		ongoingVerificationTypeName := fmt.Sprintf("%v_%v_OngoingVerification", mockTypeName, method.Name)
		args, argNames, argTypes, _ := argDataFor(method, g.packageMap, selfPackage)
		g.generateVerifierMethod(mockTypeName, typeParamNames, method, selfPackage, ongoingVerificationTypeName, args, argNames)
		g.generateOngoingVerificationType(mockTypeName, typeParams, typeParamNames, ongoingVerificationTypeName)
		g.generateOngoingVerificationGetCapturedArguments(ongoingVerificationTypeName, argNames, argTypes, typeParamNames)
		g.generateOngoingVerificationGetAllCapturedArguments(ongoingVerificationTypeName, typeParamNames, argTypes, method.Variadic != nil)
	}
}

func typeParamsStringFrom(params []*model.Parameter, packageMap map[string]string, pkgOverride string, withTypes bool) string {
	if len(params) == 0 {
		return ""
	}
	result := "["
	for i, param := range params {
		if i > 0 {
			result += ", "
		}
		result += param.Name
		if withTypes {
			result += " " + param.Type.String(packageMap, pkgOverride)
		}
	}
	return result + "]"
}

func (g *generator) generateMockType(mockTypeName string, typeParams string, typeParamNames string) {
	g.
		emptyLine().
		p("type %v%v struct {", mockTypeName, typeParams).
		p("	fail func(message string, callerSkip ...int)").
		p("}").
		emptyLine().
		p("func New%v%v(options ...pegomock.Option) *%v%v {", mockTypeName, typeParams, mockTypeName, typeParamNames).
		p("	mock := &%v%v{}", mockTypeName, typeParamNames).
		p("	for _, option := range options {").
		p("		option.Apply(mock)").
		p("	}").
		p("	return mock").
		p("}").
		emptyLine().
		p("func (mock *%v%v) SetFailHandler(fh pegomock.FailHandler) { mock.fail = fh }", mockTypeName, typeParamNames).
		p("func (mock *%v%v) FailHandler() pegomock.FailHandler      { return mock.fail }", mockTypeName, typeParamNames).
		emptyLine()
}

// If non-empty, pkgOverride is the package in which unqualified types reside.
func (g *generator) generateMockMethod(mockType string, typeParamNames string, method *model.Method, pkgOverride string) *generator {
	args, argNames, _, returnTypes := argDataFor(method, g.packageMap, pkgOverride)
	g.p("func (mock *%v%v) %v(%v) (%v) {", mockType, typeParamNames, method.Name, join(args), join(stringSliceFrom(returnTypes, g.packageMap, pkgOverride)))
	g.p("if mock == nil {").
		p("	panic(\"mock must not be nil. Use myMock := New%v().\")", mockType).
		p("}")
	g.GenerateParamsDeclaration(argNames, method.Variadic != nil)
	reflectReturnTypes := make([]string, len(returnTypes))
	for i, returnType := range returnTypes {
		reflectReturnTypes[i] = fmt.Sprintf("reflect.TypeOf((*%v)(nil)).Elem()", returnType.String(g.packageMap, pkgOverride))
	}
	resultAssignment := ""
	if len(method.Out) > 0 {
		resultAssignment = "_result :="
	}
	g.p("%v pegomock.GetGenericMockFrom(mock).Invoke(\"%v\", _params, []reflect.Type{%v})",
		resultAssignment, method.Name, strings.Join(reflectReturnTypes, ", "))
	if len(method.Out) > 0 {
		// TODO: translate LastInvocation into a Matcher so it can be used as key for Stubbings
		for i, returnType := range returnTypes {
			g.p("var _ret%v %v", i, returnType.String(g.packageMap, pkgOverride))
		}
		g.p("if len(_result) != 0 {")
		returnValues := make([]string, len(returnTypes))
		for i, returnType := range returnTypes {
			g.p("if _result[%v] != nil {", i)
			if chanType, isChanType := returnType.(*model.ChanType); isChanType && chanType.Dir != 0 {
				undirectedChanType := *chanType
				undirectedChanType.Dir = 0
				g.p("var ok bool").
					p("  _ret%v, ok = _result[%v].(%v)", i, i, undirectedChanType.String(g.packageMap, pkgOverride))
				g.p("if !ok{").
					p("_ret%v = _result[%v].(%v)", i, i, chanType.String(g.packageMap, pkgOverride)).
					p("}")
			} else {
				g.p("_ret%v  = _result[%v].(%v)", i, i, returnType.String(g.packageMap, pkgOverride))
			}
			g.p("}")
			returnValues[i] = fmt.Sprintf("_ret%v", i)
		}
		g.p("}")
		g.p("return %v", strings.Join(returnValues, ", "))
	}
	g.p("}")
	return g
}

func (g *generator) generateVerifierType(interfaceName string, typeParams string, typeParamNames string) *generator {
	return g.
		p("type Verifier%v%v struct {", interfaceName, typeParams).
		p("	mock *%v%v", interfaceName, typeParamNames).
		p("	invocationCountMatcher pegomock.InvocationCountMatcher").
		p("	inOrderContext *pegomock.InOrderContext").
		p("	timeout time.Duration").
		p("}").
		emptyLine()
}

func (g *generator) generateMockVerifyMethods(interfaceName string, typeParamNames string) {
	g.
		p("func (mock *%v%v) VerifyWasCalledOnce() *Verifier%v%v {", interfaceName, typeParamNames, interfaceName, typeParamNames).
		p("	return &Verifier%v%v{", interfaceName, typeParamNames).
		p("		mock: mock,").
		p("		invocationCountMatcher: pegomock.Times(1),").
		p("	}").
		p("}").
		emptyLine().
		p("func (mock *%v%v) VerifyWasCalled(invocationCountMatcher pegomock.InvocationCountMatcher) *Verifier%v%v {", interfaceName, typeParamNames, interfaceName, typeParamNames).
		p("	return &Verifier%v%v{", interfaceName, typeParamNames).
		p("		mock: mock,").
		p("		invocationCountMatcher: invocationCountMatcher,").
		p("	}").
		p("}").
		emptyLine().
		p("func (mock *%v%v) VerifyWasCalledInOrder(invocationCountMatcher pegomock.InvocationCountMatcher, inOrderContext *pegomock.InOrderContext) *Verifier%v%v {", interfaceName, typeParamNames, interfaceName, typeParamNames).
		p("	return &Verifier%v%v{", interfaceName, typeParamNames).
		p("		mock: mock,").
		p("		invocationCountMatcher: invocationCountMatcher,").
		p("		inOrderContext: inOrderContext,").
		p("	}").
		p("}").
		emptyLine().
		p("func (mock *%v%v) VerifyWasCalledEventually(invocationCountMatcher pegomock.InvocationCountMatcher, timeout time.Duration) *Verifier%v%v {", interfaceName, typeParamNames, interfaceName, typeParamNames).
		p("	return &Verifier%v%v{", interfaceName, typeParamNames).
		p("		mock: mock,").
		p("		invocationCountMatcher: invocationCountMatcher,").
		p("		timeout: timeout,").
		p("	}").
		p("}").
		emptyLine()
}

func (g *generator) generateVerifierMethod(interfaceName string, typeParamNames string, method *model.Method, pkgOverride string, returnTypeString string, args []string, argNames []string) *generator {
	return g.
		p("func (verifier *Verifier%v%v) %v(%v) *%v%v {", interfaceName, typeParamNames, method.Name, join(args), returnTypeString, typeParamNames).
		GenerateParamsDeclaration(argNames, method.Variadic != nil).
		p("methodInvocations := pegomock.GetGenericMockFrom(verifier.mock).Verify(verifier.inOrderContext, verifier.invocationCountMatcher, \"%v\", _params, verifier.timeout)", method.Name).
		p("return &%v%v{mock: verifier.mock, methodInvocations: methodInvocations}", returnTypeString, typeParamNames).
		p("}")
}

func (g *generator) GenerateParamsDeclaration(argNames []string, isVariadic bool) *generator {
	if isVariadic {
		return g.
			p("_params := []pegomock.Param{%v}", strings.Join(argNames[0:len(argNames)-1], ", ")).
			p("for _, param := range %v {", argNames[len(argNames)-1]).
			p("_params = append(_params, param)").
			p("}")
	} else {
		return g.p("_params := []pegomock.Param{%v}", join(argNames))
	}
}

func (g *generator) generateOngoingVerificationType(interfaceName string, typeParams string, typeParamNames string, ongoingVerificationStructName string) *generator {
	return g.
		p("type %v%v struct {", ongoingVerificationStructName, typeParams).
		p("mock *%v%v", interfaceName, typeParamNames).
		p("	methodInvocations []pegomock.MethodInvocation").
		p("}").
		emptyLine()
}

func (g *generator) generateOngoingVerificationGetCapturedArguments(ongoingVerificationStructName string, argNames []string, argTypes []string, typeParamNames string) *generator {
	g.p("func (c *%v%v) GetCapturedArguments() (%v) {", ongoingVerificationStructName, typeParamNames, join(argTypes))
	if len(argNames) > 0 {
		indexedArgNames := make([]string, len(argNames))
		for i, argName := range argNames {
			indexedArgNames[i] = argName + "[len(" + argName + ")-1]"
		}
		g.p("%v := c.GetAllCapturedArguments()", join(argNames))
		g.p("return %v", strings.Join(indexedArgNames, ", "))
	}
	g.p("}")
	g.emptyLine()
	return g
}

func (g *generator) generateOngoingVerificationGetAllCapturedArguments(ongoingVerificationStructName string, typeParamNames string, argTypes []string, isVariadic bool) *generator {
	numArgs := len(argTypes)
	argsAsArray := make([]string, numArgs)
	for i, argType := range argTypes {
		argsAsArray[i] = fmt.Sprintf("_param%v []%v", i, argType)
	}
	g.p("func (c *%v%v) GetAllCapturedArguments() (%v) {", ongoingVerificationStructName, typeParamNames, strings.Join(argsAsArray, ", "))
	if numArgs > 0 {
		g.p("_params := pegomock.GetGenericMockFrom(c.mock).GetInvocationParams(c.methodInvocations)")
		g.p("if len(_params) > 0 {")
		for i, argType := range argTypes {
			if isVariadic && i == numArgs-1 {
				variadicBasicType := strings.Replace(argType, "[]", "", 1)
				g.
					p("_param%v = make([]%v, len(c.methodInvocations))", i, argType).
					p("for u := 0; u < len(c.methodInvocations); u++ {").
					p("_param%v[u] = make([]%v, len(_params)-%v)", i, variadicBasicType, i).
					p("for x := %v; x < len(_params); x++ {", i).
					p("if _params[x][u] != nil {").
					p("_param%v[u][x-%v] = _params[x][u].(%v)", i, i, variadicBasicType).
					p("}").
					p("}").
					p("}")
				break
			} else {
				// explicitly validate the length of the params slice to avoid out of bounds code smells
				g.p("if len(_params) > %v {", i)
				g.p("_param%v = make([]%v, len(c.methodInvocations))", i, argType)
				g.p("for u, param := range _params[%v] {", i)
				g.p("_param%v[u]=param.(%v)", i, argType)
				g.p("}")
				g.p("}")
			}
		}
		g.p("}")
		g.p("return")
	}
	g.p("}")
	g.emptyLine()
	return g
}

func argDataFor(method *model.Method, packageMap map[string]string, pkgOverride string) (
	args []string,
	argNames []string,
	argTypes []string,
	returnTypes []model.Type,
) {
	args = make([]string, len(method.In))
	argNames = make([]string, len(method.In))
	argTypes = make([]string, len(args))
	for i, arg := range method.In {
		argName := arg.Name
		if argName == "" {
			argName = fmt.Sprintf("_param%d", i)
		}
		argType := arg.Type.String(packageMap, pkgOverride)
		args[i] = argName + " " + argType
		argNames[i] = argName
		argTypes[i] = argType
	}
	if method.Variadic != nil {
		argName := method.Variadic.Name
		if argName == "" {
			argName = fmt.Sprintf("_param%d", len(method.In))
		}
		argType := method.Variadic.Type.String(packageMap, pkgOverride)
		args = append(args, argName+" ..."+argType)
		argNames = append(argNames, argName)
		argTypes = append(argTypes, "[]"+argType)
	}
	returnTypes = make([]model.Type, len(method.Out))
	for i, ret := range method.Out {
		returnTypes[i] = ret.Type
	}
	return
}

func stringSliceFrom(types []model.Type, packageMap map[string]string, pkgOverride string) []string {
	result := make([]string, len(types))
	for i, t := range types {
		result[i] = t.String(packageMap, pkgOverride)
	}
	return result
}

func (g *generator) p(format string, args ...interface{}) *generator {
	fmt.Fprintf(&g.buf, format+"\n", args...)
	return g
}

func (g *generator) emptyLine() *generator { return g.p("") }

func (g *generator) formattedOutput() []byte {
	src, err := format.Source(g.buf.Bytes())
	if err != nil {
		panic(fmt.Errorf("Failed to format generated source code: %s\n%s", err, g.buf.String()))
	}
	return src
}

func join(s []string) string { return strings.Join(s, ", ") }
