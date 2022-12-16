// Copyright 2020 CUE Authors
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

package txtartest

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/pkg/encoding/json"
	"cuelang.org/go/pkg/encoding/yaml"
	"github.com/google/go-cmp/cmp"
	"github.com/grafana/thema"
	"github.com/grafana/thema/internal/envvars"
	tload "github.com/grafana/thema/load"
	"golang.org/x/tools/txtar"
)

// A CueTest represents a test run that process all CUE tests in the txtar
// format rooted in a given directory. See the [Test] documentation for
// more details.
type CueTest struct {
	// Run CueTest on this directory.
	Root string

	// Name is a unique name for this test. The golden file for this test is
	// derived from the out/<name> file in the .txtar file.
	//
	// TODO: by default derive from the current base directory name.
	Name string

	// Skip is a map of tests to skip; the key is the test name; the value is the
	// skip message.
	Skip map[string]string

	// ToDo is a map of tests that should be skipped now, but should be fixed.
	ToDo map[string]string

	// IncludeExemplars specifies whether the standard Thema lineage exemplars
	// should be included as inputs to the executed test.
	//
	// If true, the Thema exemplars will be loaded and test results will be
	// placed within Root with the naming pattern exemplar_<name>.txtar.
	IncludeExemplars bool
}

// A Test represents a single test based on a .txtar file.
//
// A Test embeds *[testing.T] and should be used to report errors.
//
// Entries within the txtar file define CUE files (available via the
// [Test.Instance] method) and expected output (or "golden") files (names
// starting with "out/\(testname)"). The "main" golden file is "out/\(testname)"
// itself, used when [Test] is used directly as an [io.Writer] and with
// [Test.WriteFile].
//
// When the test function has returned, output written with [Test.Write], [Test.Writer]
// and friends is checked against the expected output files.
//
// A txtar file can define test-specific tags and values in the comment section.
// These are available via the [Test.HasTag] and [Test.Value] methods.
// The #skip tag causes a [Test] to be skipped.
// The #noformat tag causes the $THEMA_FORMAT_TXTAR value
// to be ignored.
//
// If the output differs and $THEMA_UPDATE_GOLDEN is non-empty, the txtar file
// will be updated and written to disk with the actual output data replacing the
// out files.
//
// If $THEMA_FORMAT_TXTAR is non-empty, any CUE files in the txtar
// file will be updated to be properly formatted, unless the #noformat
// tag is present.
type Test struct {
	// Allow Test to be used as a T.
	*testing.T

	prefix   string
	buf      *bytes.Buffer // the default buffer
	outFiles []file

	Archive *txtar.Archive

	// The absolute path of the current test directory.
	Dir string

	// name of the exemplar, if they're being used
	exemplar string

	// AllowFilesetDivergence indicates whether a correctness criteria for the
	// test is that set of files in the Archive should be exactly equal to
	// the set in outFiles.
	//
	// If true, divergent filesets are not considered a failure criteria, and
	// updating golden files will not remove files from the archive.
	//
	// If true, divergent filesets are considered a failure criteria, and
	// updating golden files will remove files from the archive.
	AllowFilesetDivergence bool

	hasGold bool
}

// Write implements [io.Writer] by writing to the output for the test,
// which will be tested against the main golden file.
func (t *Test) Write(b []byte) (n int, err error) {
	if t.buf == nil {
		t.buf = &bytes.Buffer{}
		t.outFiles = append(t.outFiles, file{t.prefix, t.buf})
	}
	return t.buf.Write(b)
}

type file struct {
	name string
	buf  *bytes.Buffer
}

// HasTag reports whether the tag with the given key is defined
// for the current test. A tag x is defined by a line in the comment
// section of the txtar file like:
//
//	#x
func (t *Test) HasTag(key string) bool {
	prefix := []byte("#" + key)
	s := bufio.NewScanner(bytes.NewReader(t.Archive.Comment))
	for s.Scan() {
		b := s.Bytes()
		if bytes.Equal(bytes.TrimSpace(b), prefix) {
			return true
		}
	}
	return false
}

// Value returns the value for the given key for this test and
// reports whether it was defined.
//
// A value is defined by a line in the comment section of the txtar
// file like:
//
//	#key: value
//
// White space is trimmed from the value before returning.
func (t *Test) Value(key string) (value string, ok bool) {
	prefix := []byte("#" + key + ":")
	s := bufio.NewScanner(bytes.NewReader(t.Archive.Comment))
	for s.Scan() {
		b := s.Bytes()
		if bytes.HasPrefix(b, prefix) {
			return string(bytes.TrimSpace(b[len(prefix):])), true
		}
	}
	return "", false
}

// Bool searches for a line starting with #key: value in the comment and
// reports whether the key exists and its value is true.
func (t *Test) Bool(key string) bool {
	s, ok := t.Value(key)
	return ok && s == "true"
}

// Rel converts filename to a normalized form so that it will given the same
// output across different runs and OSes.
func (t *Test) Rel(filename string) string {
	rel, err := filepath.Rel(t.Dir, filename)
	if err != nil {
		return filepath.Base(filename)
	}
	return filepath.ToSlash(rel)
}

// WriteErrors writes the full list of errors in err to the test output.
func (t *Test) WriteErrors(err errors.Error) {
	if err != nil {
		errors.Print(t, err, &errors.Config{
			Cwd:     t.Dir,
			ToSlash: true,
		})
	}
}

// WriteFile formats f and writes it to the main output,
// prefixed by a line of the form:
//
//	== name
//
// where name is the base name of f.Filename.
func (t *Test) WriteFile(f *ast.File) {
	fmt.Fprintln(t, "==", filepath.Base(f.Filename))
	_, _ = t.Write(formatNode(t.T, f))
}

// WriteNamedFile formats f and writes it to an output corresponding
// to the f.Filename, honoring any active prefixes.
func (t *Test) WriteNamedFile(f *ast.File) {
	fmt.Fprintln(t.Writer(f.Filename), string(formatNode(t.T, f)))
}

// Writer returns a Writer with the given name. Data written will
// be checked against the file with name "out/\(testName)/\(name)"
// in the txtar file. If name is empty, data will be written to the test
// output and checked against "out/\(testName)".
func (t *Test) Writer(name string) io.Writer {
	switch name {
	case "":
		name = t.prefix
	default:
		name = path.Join(t.prefix, name)
	}

	for _, f := range t.outFiles {
		if f.name == name {
			return f.buf
		}
	}

	w := &bytes.Buffer{}
	t.outFiles = append(t.outFiles, file{name, w})

	if name == t.prefix {
		t.buf = w
	}

	return w
}

func formatNode(t *testing.T, f *ast.File) []byte {
	t.Helper()
	var str string
	var err error

	ctx := cuecontext.New()
	switch filepath.Ext(f.Filename) {
	case ".json":
		str, err = json.Marshal(ctx.BuildFile(f))
		if err == nil {
			str, err = json.Indent([]byte(str), "", "  ")
		}
	case ".yaml", ".yml":
		str, err = yaml.Marshal(ctx.BuildFile(f))
	default:
		b, terr := format.Node(f)
		str, err = string(b), terr
	}
	if err != nil {
		t.Fatal(err)
	}

	return []byte(str)
}

// BindLineage attempts to bind a lineage from the root instance in the txtar.
// By default, it will assume the entire instance is intended to be a lineage.
// However, if a #lineagePath key exists with a value, that path will be
// used instead.
func (t *Test) BindLineage(rt *thema.Runtime) thema.Lineage {
	t.Helper()
	var ctx *cue.Context
	if rt == nil {
		ctx = Context()
		rt = Runtime()
	} else {
		ctx = rt.Context()
	}

	if t.exemplar != "" {
		return getExemplars(rt)[t.exemplar]
	}

	inst := t.Instance()
	val := ctx.BuildInstance(inst)
	if p, ok := t.Value("lineagePath"); ok {
		pp := cue.ParsePath(p)
		if len(pp.Selectors()) == 0 {
			t.Fatalf("%q is not a valid value for the #lineagePath key", p)
		}
		val = val.LookupPath(pp)
		if !val.Exists() {
			t.Fatalf("path %q specified in #lineagePath does not exist in input cue instance", p)
		}
	}

	lin, err := thema.BindLineage(val, rt)
	if err != nil {
		t.Fatal(err)
	}
	return lin
}

// Instance returns the single instance representing the
// root directory in the txtar file.
func (t *Test) Instance() *build.Instance {
	return t.Instances()[0]
}

// Instances returns the valid instances for this .txtar file or skips the
// test if there is an error loading the instances.
func (t *Test) Instances(args ...string) []*build.Instance {
	t.Helper()

	a := t.RawInstances(args...)
	for _, i := range a {
		if i.Err != nil {
			if t.hasGold {
				t.Fatal("Parse error: ", i.Err)
			}
			t.Skip("Parse error: ", i.Err)
		}
	}
	return a
}

// RawInstances returns the instances represented by this .txtar file. The
// returned instances are not checked for errors.
func (t *Test) RawInstances(args ...string) []*build.Instance {
	binsts, err := Load(t.Archive, t.Name(), args...)
	if err != nil {
		t.Fatal(err)
	}
	return binsts
}

// Load loads the instance at the logical root of a txtar file.
// Relative files in the archive are given an absolute location by prefixing it with dir.
func Load(a *txtar.Archive, dir string, args ...string) ([]*build.Instance, error) {
	mfs := make(fstest.MapFS)
	for _, f := range a.Files {
		mfs[f.Name] = &fstest.MapFile{Data: f.Data}
	}

	if _, has := mfs[filepath.Join("cue.mod", "module.cue")]; !has {
		mfs[filepath.Join("cue.mod", "module.cue")] = &fstest.MapFile{Data: []byte(fmt.Sprintf(`module: "thema.test/generate"`))}
	}

	var insts []*build.Instance

	for _, arg := range append([]string{"."}, args...) {
		binst, err := tload.InstanceWithThema(mfs, arg)
		if err != nil {
			return nil, err
		}
		insts = append(insts, binst)
	}
	return insts, nil
}

// Run runs tests defined in txtar files in x.Root or its subdirectories.
//
// The function f is called for each such txtar file. See the [Test] documentation
// for more details.
func (x *CueTest) Run(t *testing.T, f func(tc *Test)) {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	root := x.Root

	all := make(map[string]thema.Lineage)
	if x.IncludeExemplars {
		all = getExemplars(nil)
	}
	ents, err := os.ReadDir(x.Root)
	if err != nil {
		t.Fatalf("could not read root dir %s: %s", x.Root, err)
	}

	for _, ent := range ents {
		name := ent.Name()
		if path.Ext(name) == ".txtar" && strings.HasPrefix(name, "exemplar_") {
			if !x.IncludeExemplars {
				t.Fail()
				t.Logf("%s: test files with prefix exemplar_ are reserved for exemplar testing with CueTest.IncludeExemplars=true", name)
			} else {
				name = name[9:len(name)-6]
				fmt.Println(name)
				if _, has := all[name]; has {
					delete(all, name)
				} else {
					t.Fail()
					t.Logf("%s: no exemplar exists with name %q, file must be removed", ent.Name(), name)
				}
			}
		}
	}

	if t.Failed() {
		t.FailNow()
	}

	if x.IncludeExemplars {
		for name := range all {
			os.Create(filepath.Join(x.Root, fmt.Sprintf("exemplar_%s.txtar", name)))
		}
	}

	err = filepath.WalkDir(root, func(fullpath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(fullpath) != ".txtar" {
			return nil
		}

		str := filepath.ToSlash(fullpath)
		p := strings.Index(str, "/testdata/")
		testName := str[p+len("/testdata/") : len(str)-len(".txtar")]

		t.Run(testName, func(t *testing.T) {
			a, err := txtar.ParseFile(fullpath)
			if err != nil {
				t.Fatalf("error parsing txtar file: %v", err)
			}

			tc := &Test{
				T:       t,
				Archive: a,
				Dir:     filepath.Dir(filepath.Join(dir, fullpath)),
				exemplar: exemplarNameFromPath(fullpath),

				prefix: path.Join("out", x.Name),
			}

			if tc.HasTag("skip") {
				t.Skip()
			}

			if msg, ok := x.Skip[testName]; ok {
				t.Skip(msg)
			}
			if msg, ok := x.ToDo[testName]; ok {
				t.Skip(msg)
			}

			update := false

			for i, f := range a.Files {

				if strings.HasPrefix(f.Name, tc.prefix) && (f.Name == tc.prefix || f.Name[len(tc.prefix)] == '/') {
					// It's either "\(tc.prefix)" or "\(tc.prefix)/..." but not some other name
					// that happens to start with tc.prefix.
					tc.hasGold = true
				}

				// Format CUE files as required
				if tc.HasTag("noformat") || !strings.HasSuffix(f.Name, ".cue") {
					continue
				}
				if ff, err := format.Source(f.Data); err == nil {
					if bytes.Equal(f.Data, ff) {
						continue
					}
					if envvars.FormatTxtar {
						update = true
						a.Files[i].Data = ff
					}
				}
			}

			f(tc)

			// Construct index of files in general, and set of files associated
			// with this test by prefix
			index := make(map[string]int, len(a.Files))
			indexthis := make(map[string]bool, len(a.Files))
			for i, f := range a.Files {
				index[f.Name] = i
				if strings.HasPrefix(f.Name, tc.prefix) {
					indexthis[f.Name] = true
				}
			}

			// Insert results of this test at first location of any existing
			// test or at end of list otherwise.
			k := len(a.Files)
			for _, sub := range tc.outFiles {
				if i, ok := index[sub.name]; ok {
					k = i
					break
				}
			}

			files := a.Files[:k:k]

			// Walk all files created during this test, comparing them against
			// archive contents
			for _, sub := range tc.outFiles {
				result := sub.buf.Bytes()

				files = append(files, txtar.File{Name: sub.name})
				gold := &files[len(files)-1]

				if i, ok := index[sub.name]; ok {
					gold.Data = a.Files[i].Data
					delete(index, sub.name)
					delete(indexthis, sub.name)

					if bytes.Equal(gold.Data, result) {
						continue
					}
				} else if !tc.AllowFilesetDivergence && !envvars.UpdateGoldenFiles {
					t.Fail()
					t.Logf("result %s does not exist (rerun with %s=1 to fix)", sub.name, envvars.VarUpdateGolden)
					continue
				}

				if envvars.UpdateGoldenFiles {
					update = true
					gold.Data = result
					continue
				}

				t.Errorf("result for %s differs:\n%s",
					sub.name,
					cmp.Diff(string(gold.Data), string(result)))
			}

			// Add remaining unrelated files, ignoring files that were already
			// added.
			for _, f := range a.Files[k:] {
				if _, ok := index[f.Name]; ok {
					files = append(files, f)
				}
			}
			a.Files = files

			if !tc.AllowFilesetDivergence && len(indexthis) != 0 {
				if !envvars.UpdateGoldenFiles {
					var extra []string
					for name := range indexthis {
						extra = append(extra, name)
					}
					t.Errorf("output not generated by test (rerun with %s=1 to fix):\n\t%s\n", envvars.VarUpdateGolden, strings.Join(extra, "\n\t"))
				} else {
					update = true
					// Remove files no longer generated by test
					filtered := make([]txtar.File, 0, len(a.Files)-len(indexthis))
					for _, f := range a.Files {
						if !indexthis[f.Name] {
							filtered = append(filtered, f)
						}
					}
					a.Files = filtered
				}
			}

			if update {
				sort.Slice(a.Files, func(i, j int) bool {
					return a.Files[i].Name < a.Files[j].Name
				})
				err = ioutil.WriteFile(fullpath, txtar.Format(a), 0644)
				if err != nil {
					t.Fatal(err)
				}
			}
		})

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}
}