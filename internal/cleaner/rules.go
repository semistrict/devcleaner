package cleaner

import (
	"os"
	"path/filepath"
	"strings"
)

// Rules recognize Git-ignored artifacts only. A matching name never grants
// permission to remove tracked files, a repository, or a symbolic-link target.
type ArtifactRule struct {
	Tool    string   `json:"tool"`
	Names   []string `json:"directory_names"`
	Markers []string `json:"project_markers,omitempty"`
	Safety  string   `json:"safety"`
	Note    string   `json:"note"`
}

var ArtifactRules = []ArtifactRule{
	{"JavaScript", []string{"node_modules", ".next", ".nuxt", ".turbo", ".parcel-cache", ".svelte-kit", ".angular"}, nil, "safe", "Dependencies and framework caches; reinstall or rebuild."},
	{"JavaScript", []string{"dist", "build"}, []string{"package.json"}, "review", "Common output names, but custom build scripts may put local data here."},
	{"Python", []string{"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".hypothesis"}, nil, "safe", "Interpreter and test/type-checker caches."},
	{"Python", []string{".venv", "venv", ".tox", ".nox"}, []string{"pyproject.toml", "requirements.txt", "setup.py", "Pipfile"}, "review", "Environments may contain manually installed or edited packages."},
	{"Rust", []string{"target"}, []string{"Cargo.toml"}, "safe", "Cargo compilation artifacts."},
	{"Swift", []string{".build"}, []string{"Package.swift"}, "safe", "Swift Package Manager build products and checkouts."},
	{"Maven", []string{"target"}, []string{"pom.xml"}, "safe", "Maven compilation and test output."},
	{"Gradle", []string{"build", ".gradle"}, []string{"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"}, "safe", "Gradle build output and project caches."},
	{".NET", []string{"bin", "obj"}, []string{"*.csproj", "*.fsproj", "*.vbproj"}, "safe", "MSBuild intermediate and compiled output."},
	{"Dart / Flutter", []string{".dart_tool", "build"}, []string{"pubspec.yaml"}, "safe", "Dart tooling and Flutter compilation output."},
	{"Elixir", []string{"_build", "deps", ".elixir_ls"}, []string{"mix.exs"}, "safe", "Mix dependencies, compilation output, and language-server cache."},
	{"Zig", []string{".zig-cache", "zig-cache", "zig-out"}, []string{"build.zig"}, "safe", "Zig compiler caches and generated output."},
	{"Ruby", []string{"vendor/bundle"}, []string{"Gemfile"}, "review", "Bundler packages may contain local modifications."},
	{"C / C++", []string{"CMakeFiles"}, nil, "safe", "CMake-generated intermediate files."},
}

type CacheRule struct {
	Tool         string `json:"tool"`
	RelativePath string `json:"home_relative_path"`
	Safety       string `json:"safety"`
}

var CacheRules = []CacheRule{
	{"Xcode", "Library/Developer/Xcode/DerivedData", "safe"},
	{"Homebrew", "Library/Caches/Homebrew", "safe"},
	{"pip", "Library/Caches/pip", "safe"},
	{"uv", "Library/Caches/uv", "safe"},
	{"uv", ".cache/uv", "safe"},
	{"Lima downloads", "Library/Caches/lima", "safe"},
	{"Bazel", "Library/Caches/bazel", "safe"},
	{"Bazelisk", "Library/Caches/bazelisk", "safe"},
	{"Playwright browsers", "Library/Caches/ms-playwright", "safe"},
	{"Cypress", "Library/Caches/Cypress", "safe"},
	{"Electron downloads", "Library/Caches/electron", "safe"},
	{"CocoaPods", "Library/Caches/CocoaPods", "safe"},
	{"Dotslash", "Library/Caches/dotslash", "safe"},
	{"gopls", "Library/Caches/gopls", "safe"},
	{"TinyGo", "Library/Caches/tinygo", "safe"},
	{"Zig", ".cache/zig", "safe"},
	{"Go", "Library/Caches/go-build", "safe"},
	{"Go modules", "go/pkg/mod", "review"},
	{"pnpm", "Library/pnpm/store", "safe"},
	{"Yarn", "Library/Caches/Yarn", "safe"},
	{"JavaScript package cache", ".npm/_cacache", "safe"},
	{"Gradle", ".gradle/caches", "safe"},
	{"Maven", ".m2/repository", "review"},
	{"Rust registry", ".cargo/registry", "safe"},
	{"Dart packages", ".pub-cache", "review"},
}

func classifyIgnored(path string) (safety, tool string) {
	if info, err := os.Lstat(path); err != nil || !info.IsDir() {
		return "unsafe", "Unrecognized ignored data"
	}
	for _, r := range ArtifactRules {
		for _, name := range r.Names {
			// Multi-component names such as vendor/bundle are rooted at the project.
			components := len(strings.Split(name, "/"))
			project := path
			for range components {
				project = filepath.Dir(project)
			}
			if filepath.Join(project, filepath.FromSlash(name)) != path {
				continue
			}
			if len(r.Markers) == 0 {
				return r.Safety, r.Tool
			}
			for _, marker := range r.Markers {
				matches, _ := filepath.Glob(filepath.Join(project, marker))
				for _, m := range matches {
					if info, err := os.Stat(m); err == nil && !info.IsDir() {
						return r.Safety, r.Tool
					}
				}
			}
		}
	}
	return "unsafe", "Unrecognized ignored data"
}
func ignoredRisk(path string) string { level, _ := classifyIgnored(path); return level }
func SafetyRank(level string) int {
	switch level {
	case "safe":
		return 0
	case "review":
		return 1
	case "unsafe":
		return 2
	default:
		return -1
	}
}
func cacheRule(path, home string) (CacheRule, bool) {
	for _, r := range CacheRules {
		if path == filepath.Join(home, r.RelativePath) {
			return r, true
		}
	}
	return CacheRule{}, false
}
