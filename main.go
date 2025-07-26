package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Regex to find `use` and `mod` statements.
var (
	useRegex = regexp.MustCompile(`^\s*use\s+(?:super::|crate::)?(\w+)`)
	modRegex = regexp.MustCompile(`^\s*(?:pub\s+)?mod\s+(\w+);`) // Handles `pub mod`
)

const (
	ColorReset   = "\033[0m"
	ColorBold    = "\033[1m"
	ColorCyan    = "\033[36m"
	ColorYellow  = "\033[33m"
	ColorGreen   = "\033[32m"
	ColorBlue    = "\033[34m"
	ColorMagenta = "\033[35m"
	ColorGray    = "\033[90m"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <directory>")
		fmt.Println("Example: go run main.go ./my-rust-project/src")
		os.Exit(1)
	}
	rootDir := os.Args[1]

	// 1. Find all potential modules and map their names to file paths.
	moduleFileMap, err := findModuleFiles(rootDir)
	if err != nil {
		log.Fatalf("Error finding module files: %v", err)
	}

	// 2. Parse files to build the dependency graphs.
	folderModules, dependencies, err := analyzeDependencies(rootDir, moduleFileMap)
	if err != nil {
		log.Fatalf("Error analyzing dependencies: %v", err)
	}

	// 3. Print the results in a new, enhanced format.
	printResults(folderModules, dependencies, rootDir)
}

// findModuleFiles walks the directory and creates a map of module names to their full paths.
func findModuleFiles(root string) (map[string]string, error) {
	modules := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".rs") {
			modules[getModuleNameFromFilePath(path)] = path
		}
		return nil
	})
	return modules, err
}

// getModuleNameFromFilePath is a helper to derive a module name from a file path.
func getModuleNameFromFilePath(path string) string {
	if strings.HasSuffix(path, "mod.rs") || strings.HasSuffix(path, "lib.rs") {
		return filepath.Base(filepath.Dir(path))
	}
	return strings.TrimSuffix(filepath.Base(path), ".rs")
}

// analyzeDependencies parses files for `use`/`mod` to build dependency and folder-module graphs.
func analyzeDependencies(root string, moduleFileMap map[string]string) (map[string][]string, map[string]map[string]struct{}, error) {
	folderModules := make(map[string][]string)
	deps := make(map[string]map[string]struct{})

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".rs") {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		deps[path] = make(map[string]struct{})
		scanner := bufio.NewScanner(file)
		currentModuleName := getModuleNameFromFilePath(path)

		for scanner.Scan() {
			line := scanner.Text()

			if modMatch := modRegex.FindStringSubmatch(line); modMatch != nil {
				childModuleName := modMatch[1]
				folderModules[currentModuleName] = append(folderModules[currentModuleName], childModuleName)
			} else if useMatch := useRegex.FindStringSubmatch(line); useMatch != nil {
				depModuleName := useMatch[1]
				if _, ok := moduleFileMap[depModuleName]; ok {
					deps[path][depModuleName] = struct{}{}
				}
			}
		}
		return scanner.Err()
	})

	// Deduplicate children in folderModules
	for parent, children := range folderModules {
		seen := make(map[string]bool)
		var uniqueChildren []string
		for _, child := range children {
			if !seen[child] {
				seen[child] = true
				uniqueChildren = append(uniqueChildren, child)
			}
		}
		sort.Strings(uniqueChildren)
		folderModules[parent] = uniqueChildren
	}

	return folderModules, deps, err
}

// wrapItems takes a list of strings and formats them into lines that fit a max width.
func wrapItems(items []string, maxWidth int, separator string) []string {
	if len(items) == 0 {
		return []string{""}
	}

	var lines []string
	var currentLine string
	for i, item := range items {
		if i == 0 {
			currentLine = item
			continue
		}
		if len(currentLine)+len(separator)+len(item) > maxWidth {
			lines = append(lines, currentLine)
			currentLine = item
		} else {
			currentLine += separator + item
		}
	}
	lines = append(lines, currentLine)

	return lines
}

// printResults displays all analysis results in a user-friendly, colored, table format.
func printResults(folderModules map[string][]string, dependencies map[string]map[string]struct{}, rootDir string) {
	fmt.Printf("\n%s🔍 Rust Dependency Analysis %s\n", ColorBold, ColorReset)
	fmt.Printf("%sTarget Directory: %s%s%s\n\n", ColorGray, ColorReset, ColorBlue, rootDir)

	printHierarchyTable(folderModules)
	printOutboundTable(dependencies)
	printInboundTable(dependencies)

	fmt.Printf("\n%s✨ Analysis Complete.%s\n\n", ColorGreen, ColorReset)
}

// printHierarchyTable displays parent-child module relationships with text wrapping.
func printHierarchyTable(folderModules map[string][]string) {
	fmt.Printf("%s## 📂 Module Hierarchy (Parent Module → Contains)%s\n", ColorBold, ColorReset)
	col1Width, col2Width := 17, 60
	var parents []string
	for parent, children := range folderModules {
		if len(children) > 0 {
			parents = append(parents, parent)
			if len(parent) > col1Width {
				col1Width = len(parent)
			}
		}
	}
	sort.Strings(parents)

	// Print table header
	fmt.Printf("%s┌─%s─┬─%s─┐%s\n", ColorGray, strings.Repeat("─", col1Width), strings.Repeat("─", col2Width), ColorReset)
	fmt.Printf("%s│ %s%-*s%s │ %s%-*s%s │%s\n", ColorGray, ColorBold, col1Width, " Parent Module", ColorGray, ColorBold, col2Width, " Contains Modules", ColorGray, ColorReset)

	// Print table rows
	for _, parent := range parents {
		fmt.Printf("%s├─%s─┼─%s─┤%s\n", ColorGray, strings.Repeat("─", col1Width), strings.Repeat("─", col2Width), ColorReset)
		children := folderModules[parent]
		wrappedLines := wrapItems(children, col2Width, ", ")

		fmt.Printf("%s│%s %-*s %s│%s %-*s %s│%s\n", ColorGray, ColorYellow, col1Width, parent, ColorGray, ColorReset, col2Width, wrappedLines[0], ColorGray, ColorReset)
		for i := 1; i < len(wrappedLines); i++ {
			fmt.Printf("%s│%s %-*s %s│%s %-*s %s│%s\n", ColorGray, " ", col1Width, "", ColorGray, ColorReset, col2Width, wrappedLines[i], ColorGray, ColorReset)
		}
	}

	fmt.Printf("%s└─%s─┴─%s─┘%s\n\n", ColorGray, strings.Repeat("─", col1Width), strings.Repeat("─", col2Width), ColorReset)
}

// printOutboundTable displays what modules each file depends on with text wrapping.
func printOutboundTable(dependencies map[string]map[string]struct{}) {
	fmt.Printf("%s## 📤 Outbound Dependencies (File → Uses)%s\n", ColorBold, ColorReset)
	col1Width, col2Width := 12, 70
	var sortedFiles []string
	for file, deps := range dependencies {
		if len(deps) > 0 {
			sortedFiles = append(sortedFiles, file)
			if len(filepath.Base(file)) > col1Width {
				col1Width = len(filepath.Base(file))
			}
		}
	}
	sort.Strings(sortedFiles)

	fmt.Printf("%s┌─%s─┬─%s─┐%s\n", ColorGray, strings.Repeat("─", col1Width), strings.Repeat("─", col2Width), ColorReset)
	fmt.Printf("%s│ %s%-*s%s │ %s%-*s%s │%s\n", ColorGray, ColorBold, col1Width, " File", ColorGray, ColorBold, col2Width, " Uses Modules", ColorGray, ColorReset)

	for _, file := range sortedFiles {
		fmt.Printf("%s├─%s─┼─%s─┤%s\n", ColorGray, strings.Repeat("─", col1Width), strings.Repeat("─", col2Width), ColorReset)
		var depsList []string
		for dep := range dependencies[file] {
			depsList = append(depsList, dep)
		}
		sort.Strings(depsList)
		wrappedLines := wrapItems(depsList, col2Width, ", ")

		fmt.Printf("%s│%s %-*s %s│%s %-*s %s│%s\n", ColorGray, ColorBlue, col1Width, filepath.Base(file), ColorGray, ColorReset, col2Width, wrappedLines[0], ColorGray, ColorReset)
		for i := 1; i < len(wrappedLines); i++ {
			fmt.Printf("%s│%s %-*s %s│%s %-*s %s│%s\n", ColorGray, " ", col1Width, "", ColorGray, ColorReset, col2Width, wrappedLines[i], ColorGray, ColorReset)
		}
	}

	fmt.Printf("%s└─%s─┴─%s─┘%s\n\n", ColorGray, strings.Repeat("─", col1Width), strings.Repeat("─", col2Width), ColorReset)
}

// printInboundTable displays which modules are most depended-on with corrected text wrapping.
func printInboundTable(dependencies map[string]map[string]struct{}) {
	fmt.Printf("%s## 📥 Inbound Dependencies (Module ← Used By)%s\n", ColorBold, ColorReset)
	type moduleDepInfo struct {
		name       string
		count      int
		dependents []string
	}
	inbound := make(map[string][]string)
	for file, deps := range dependencies {
		for dep := range deps {
			// Deduplicate file entries for the same module dependency
			isAlreadyPresent := false
			for _, existingFile := range inbound[dep] {
				if existingFile == filepath.Base(file) {
					isAlreadyPresent = true
					break
				}
			}
			if !isAlreadyPresent {
				inbound[dep] = append(inbound[dep], filepath.Base(file))
			}
		}
	}

	var inboundList []moduleDepInfo
	col1Width, col3Width := 10, 50 // Default widths for Module and Used By Files columns
	for module, files := range inbound {
		sort.Strings(files)
		inboundList = append(inboundList, moduleDepInfo{name: module, count: len(files), dependents: files})
		if len(module) > col1Width {
			col1Width = len(module)
		}
	}
	// Sort by dependency count (desc) and then name (asc)
	sort.Slice(inboundList, func(i, j int) bool {
		if inboundList[i].count != inboundList[j].count {
			return inboundList[i].count > inboundList[j].count
		}
		return inboundList[i].name < inboundList[j].name
	})

	// Print table header
	fmt.Printf("%s┌─%s─┬──────────────┬─%s─┐%s\n", ColorGray, strings.Repeat("─", col1Width), strings.Repeat("─", col3Width), ColorReset)
	fmt.Printf("%s│ %s%-*s%s │%s %-12s %s│ %s%-*s%s │%s\n", ColorGray, ColorBold, col1Width, " Module", ColorGray, ColorBold, "Dep. Count", ColorGray, ColorBold, col3Width, " Used By Files", ColorGray, ColorReset)

	// Print table rows
	for _, info := range inboundList {
		fmt.Printf("%s├─%s─┼──────────────┼─%s─┤%s\n", ColorGray, strings.Repeat("─", col1Width), strings.Repeat("─", col3Width), ColorReset)
		wrappedLines := wrapItems(info.dependents, col3Width, ", ")
		countStr := strconv.Itoa(info.count)

		// Print the first line with all module info
		fmt.Printf("%s│%s %-*s %s│%s     %-7s %s│%s %-*s %s│%s\n",
			ColorGray, ColorYellow, col1Width, info.name,
			ColorGray, ColorGreen, countStr,
			ColorGray, ColorReset, col3Width, wrappedLines[0],
			ColorGray, ColorReset,
		)

		// CORRECTED: Print subsequent wrapped lines with proper alignment
		for i := 1; i < len(wrappedLines); i++ {
			fmt.Printf("%s│ %-*s │              │%s %-*s %s│%s\n",
				ColorGray,                        // Left border color
				col1Width, "",                    // Column 1: empty and padded
				ColorReset,                       // Column 3: reset color before text
				col3Width, wrappedLines[i],       // Column 3: the wrapped text
				ColorGray,                        // Right border color
				ColorReset,                       // Final reset
			)
		}
	}

	// Print table footer
	fmt.Printf("%s└─%s─┴──────────────┴─%s─┘%s\n", ColorGray, strings.Repeat("─", col1Width), strings.Repeat("─", col3Width), ColorReset)
}