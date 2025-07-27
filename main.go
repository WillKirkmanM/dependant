package main

import (
	"bytes"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	usePathRegex = regexp.MustCompile(`use\s+(crate|super)(::[\s\S]*?;)`)
	commentRegex = regexp.MustCompile(`//.*`)
	pubDefRegex  = regexp.MustCompile(`pub\s+(?:struct|enum|fn|trait)\s+(\w+)`)
)

type ModuleInfo struct { Name, ID, CountStr string; Dependents []string }
type ItemInfo struct { ModuleName, Name, CountStr string; Files []string }
type TemplateData struct {
	TargetDir            string
	AllModules           []ModuleInfo
	TopImportedItems     []ItemInfo
	PerModuleItemImports map[string][]ItemInfo
}

func main() {
	if len(os.Args) < 2 { fmt.Println("Usage: go run main.go <directory>"); os.Exit(1) }
	rootDir := os.Args[1]

	symbolTable, err := buildSymbolTable(rootDir)
	if err != nil { log.Fatalf("Error building symbol table: %v", err) }

	dependencies, itemImports, err := analyzeDependencies(rootDir, symbolTable)
	if err != nil { log.Fatalf("Error analyzing dependencies: %v", err) }

	htmlContent, err := generateHTMLReport(dependencies, itemImports, rootDir)
	if err != nil { log.Fatalf("Error generating HTML report: %v", err) }
	
	serveAndOpen(htmlContent)
}

// --- Pass 1: Symbol Table Builder ---
func buildSymbolTable(root string) (map[string]map[string]struct{}, error) {
	table := make(map[string]map[string]struct{})
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".rs") { return err }
		content, err := os.ReadFile(path)
		if err != nil { return err }
		moduleName := getModuleNameFromFilePath(path)
		if _, ok := table[moduleName]; !ok { table[moduleName] = make(map[string]struct{}) }
		matches := pubDefRegex.FindAllStringSubmatch(string(content), -1)
		for _, match := range matches { if len(match) > 1 { table[moduleName][match[1]] = struct{}{} } }
		return nil
	})
	return table, err
}

// --- Pass 2: Dependency Analyzer with NEW Parsing Engine ---
func analyzeDependencies(root string, symbolTable map[string]map[string]struct{}) (map[string]map[string]struct{}, map[string]map[string]map[string]struct{}, error) {
	deps := make(map[string]map[string]struct{})
	itemImports := make(map[string]map[string]map[string]struct{})

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".rs") { return err }
		contentBytes, err := os.ReadFile(path)
		if err != nil { return err }

		fileContent := string(contentBytes)
		contentWithoutComments := commentRegex.ReplaceAllString(fileContent, "")
		
		allMatches := usePathRegex.FindAllStringSubmatch(contentWithoutComments, -1)
		for _, match := range allMatches {
			usePrefix := match[1] // "crate" or "super"
			fullPath := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(match[2], "::"), ";"))
			
			var initialPrefix []string
			if usePrefix == "super" {
				initialPrefix = []string{filepath.Base(filepath.Dir(path))}
			}

			// Start the new recursive parsing process
			parseUsePathRecursive(fullPath, initialPrefix, path, fileContent, deps, itemImports, symbolTable)
		}
		return nil
	})
	return deps, itemImports, err
}

func parseUsePathRecursive(pathStr string, prefixParts []string, filePath, fileContent string, deps map[string]map[string]struct{}, itemImports map[string]map[string]map[string]struct{}, symbolTable map[string]map[string]struct{}) {
	pathStr = strings.TrimSpace(pathStr)
	if pathStr == "" { return }

	// Handle groups like `{a, b::{c, d}}`
	if strings.HasPrefix(pathStr, "{") {
		for _, subPath := range splitUseGroup(pathStr) {
			parseUsePathRecursive(subPath, prefixParts, filePath, fileContent, deps, itemImports, symbolTable)
		}
		return
	}

	// Handle path segments like `cpu::items::{a, b}`
	if head, tail, found := strings.Cut(pathStr, "::"); found {
		newPrefix := append(prefixParts, head)
		parseUsePathRecursive(tail, newPrefix, filePath, fileContent, deps, itemImports, symbolTable)
		return
	}

	// Base case: we have a final item (e.g., `Engine`, `*`, or `self`)
	itemName := strings.TrimSpace(strings.Split(pathStr, " as ")[0])
	if itemName == "self" || itemName == "" { return }

	if len(prefixParts) == 0 { return } // Should not happen with `crate` or `super`
	moduleName := prefixParts[0]

	// Register module dependency
	if deps[filePath] == nil { deps[filePath] = make(map[string]struct{}) }
	deps[filePath][moduleName] = struct{}{}

	if _, ok := itemImports[moduleName]; !ok { itemImports[moduleName] = make(map[string]map[string]struct{}) }

	// Handle glob or specific item
	if itemName == "*" {
		if publicSymbols, ok := symbolTable[moduleName]; ok {
			for symbol := range publicSymbols {
				if r, err := regexp.Compile(`\b` + symbol + `\b`); err == nil && r.MatchString(fileContent) {
					if _, ok := itemImports[moduleName][symbol]; !ok { itemImports[moduleName][symbol] = make(map[string]struct{}) }
					itemImports[moduleName][symbol][filePath] = struct{}{}
				}
			}
		}
	} else {
		if _, ok := itemImports[moduleName][itemName]; !ok { itemImports[moduleName][itemName] = make(map[string]struct{}) }
		itemImports[moduleName][itemName][filePath] = struct{}{}
	}
}

func splitUseGroup(group string) []string {
	// Expects input WITH outer braces, e.g., "{ a, b::{c,d}, e, }"
	if !strings.HasPrefix(group, "{") || !strings.HasSuffix(group, "}") {
		return []string{group}
	}
	content := group[1 : len(group)-1]

	var paths []string
	braceLevel := 0
	lastSplit := 0
	for i, char := range content {
		switch char {
		case '{': braceLevel++
		case '}': braceLevel--
		case ',':
			if braceLevel == 0 {
				paths = append(paths, strings.TrimSpace(content[lastSplit:i]))
				lastSplit = i + 1
			}
		}
	}
	// Add the final part of the string after the last comma.
	if lastSplit <= len(content) {
		paths = append(paths, strings.TrimSpace(content[lastSplit:]))
	}
	
	var finalPaths []string
	for _, p := range paths { if p != "" { finalPaths = append(finalPaths, p) } }
	return finalPaths
}

func getModuleNameFromFilePath(path string) string {
	if strings.HasSuffix(path, "mod.rs") || strings.HasSuffix(path, "lib.rs") { return filepath.Base(filepath.Dir(path)) }
	return strings.TrimSuffix(filepath.Base(path), ".rs")
}

func generateHTMLReport(dependencies map[string]map[string]struct{}, itemImports map[string]map[string]map[string]struct{}, rootDir string) (string, error) {
	inbound := make(map[string][]string); for file, deps := range dependencies { for dep := range deps { inbound[dep] = append(inbound[dep], filepath.Base(file)) } }
	var allModules []ModuleInfo
	for module, files := range inbound {
		if module == "" { continue }
		fileSet := make(map[string]struct{}); for _, f := range files { fileSet[f] = struct{}{} }
		uniqueFiles := []string{}; for f := range fileSet { uniqueFiles = append(uniqueFiles, f) }
		sort.Strings(uniqueFiles)
		allModules = append(allModules, ModuleInfo{Name: module, ID: "module-" + module, CountStr: fmt.Sprintf("%d", len(uniqueFiles)), Dependents: uniqueFiles})
	}
	sort.Slice(allModules, func(i, j int) bool {
		c1, _ := strconv.Atoi(allModules[i].CountStr); c2, _ := strconv.Atoi(allModules[j].CountStr)
		if c1 != c2 { return c1 > c2 }; return allModules[i].Name < allModules[j].Name
	})

	var topImportedItems []ItemInfo
	perModuleItemImports := make(map[string][]ItemInfo)
	var sortedModuleNames []string
	for module := range itemImports { if len(itemImports[module]) > 0 { sortedModuleNames = append(sortedModuleNames, module) } }
	sort.Strings(sortedModuleNames)
	for _, module := range sortedModuleNames {
		var items []ItemInfo
		for name, fileSet := range itemImports[module] {
			var files []string
			for f := range fileSet { files = append(files, filepath.Base(f)) }
			sort.Strings(files)
			item := ItemInfo{ModuleName: module, Name: name, CountStr: fmt.Sprintf("%d", len(files)), Files: files}
			items = append(items, item)
			topImportedItems = append(topImportedItems, item)
		}
		sort.Slice(items, func(i, j int) bool {
			c1, _ := strconv.Atoi(items[i].CountStr); c2, _ := strconv.Atoi(items[j].CountStr)
			if c1 != c2 { return c1 > c2 }; return items[i].Name < items[j].Name
		})
		perModuleItemImports[module] = items
	}
	sort.Slice(topImportedItems, func(i, j int) bool {
		c1, _ := strconv.Atoi(topImportedItems[i].CountStr); c2, _ := strconv.Atoi(topImportedItems[j].CountStr)
		if c1 != c2 { return c1 > c2 }; return topImportedItems[i].ModuleName < topImportedItems[j].ModuleName
	})

	data := TemplateData{ TargetDir: rootDir, AllModules: allModules, TopImportedItems: topImportedItems, PerModuleItemImports: perModuleItemImports }
	tmpl, err := template.New("report").Funcs(template.FuncMap{ "join": func(s []string) string { return strings.Join(s, ", ") }}).Parse(htmlTemplate)
	if err != nil { return "", err }
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil { return "", err }
	return buf.String(), nil
}

func serveAndOpen(htmlContent string) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil { log.Fatalf("Could not find an available port: %v", err) }
	port := listener.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	shutdown := make(chan struct{})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html"); fmt.Fprint(w, htmlContent); close(shutdown)
	})
	fmt.Printf("✅ Analysis complete. Opening report in your browser at %s\n", url)
	if err := openBrowser(url); err != nil { log.Printf("Could not open browser automatically: %v. Please open this URL manually: %s", err, url) }
	go func() { if err := http.Serve(listener, nil); err != http.ErrServerClosed { log.Fatalf("Server error: %v", err) } }()
	select {
	case <-shutdown: time.Sleep(100 * time.Millisecond)
	case <-time.After(30 * time.Second): log.Println("Timed out waiting for page to be loaded.")
	}
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin": cmd = exec.Command("open", url)
	case "linux": cmd = exec.Command("xdg-open", url)
	case "windows": cmd = exec.Command("cmd", "/c", "start", strings.Replace(url, "&", "^&", -1))
	default: return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Run()
}

const htmlTemplate = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>Rust Dependency Analysis Report</title>
    <link rel="preconnect" href="https://fonts.googleapis.com"><link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;700&family=Fira+Code:wght@400;500&display=swap" rel="stylesheet">
    <style>
        :root { --bg-color: #1a1b26; --card-bg: #24283b; --border-color: #3b4261; --text-color: #c0caf5; --heading-color: #ffffff; --green: #9ece6a; --yellow: #e0af68; --blue: #7aa2f7; --magenta: #bb9af7; --cyan: #7dcfff; --font-sans: 'Inter', sans-serif; --font-mono: 'Fira Code', monospace; }
        html { scroll-behavior: smooth; }
        body { background-color: var(--bg-color); color: var(--text-color); font-family: var(--font-sans); margin: 0; padding: 2rem; line-height: 1.6; }
        .container { max-width: 1200px; margin: 0 auto; }
        header { text-align: center; margin-bottom: 2rem; }
        header h1 { font-size: 2.5rem; color: var(--heading-color); font-weight: 700; margin: 0; }
        header .target-dir { font-family: var(--font-mono); color: var(--cyan); background-color: var(--card-bg); padding: 0.25rem 0.5rem; border-radius: 6px; display: inline-block; margin-top: 0.5rem; }
		nav { background-color: var(--card-bg); border: 1px solid var(--border-color); padding: 1rem 1.5rem; margin-bottom: 2.5rem; border-radius: 8px; }
		nav h3 { margin: 0 0 0.75rem 0; font-size: 1rem; color: var(--heading-color); text-align: center; }
		.nav-links { display: flex; flex-wrap: wrap; justify-content: center; gap: 0.4rem 0.8rem; }
		nav a { color: var(--blue); text-decoration: none; font-size: 0.9rem; font-family: var(--font-mono); transition: color 0.2s; background-color: var(--bg-color); padding: 0.2rem 0.5rem; border-radius: 4px; }
		nav a:hover { color: var(--cyan); }
        .analysis-section { background-color: var(--card-bg); border: 1px solid var(--border-color); border-radius: 8px; margin-bottom: 2.5rem; overflow: hidden; }
        .analysis-section > h2 { font-size: 1.5rem; color: var(--heading-color); margin: 0; padding: 1rem 1.5rem; border-bottom: 1px solid var(--border-color); }
        .table-container { overflow-x: auto; padding: 0.5rem 0 0.5rem 0; }
		.table-container table { margin: 0 1.5rem; width: calc(100% - 3rem); }
        table { border-collapse: collapse; font-size: 0.95rem; }
        th, td { padding: 0.85rem 1rem; text-align: left; border-bottom: 1px solid var(--border-color); }
        thead th { font-weight: 500; color: var(--heading-color); font-size: 1rem; white-space: nowrap; }
        tbody tr:last-child td { border-bottom: none; }
        .module-name, .item-name { color: var(--yellow); font-family: var(--font-mono); }
        .dep-count { color: var(--green); font-weight: 500; font-family: var(--font-mono); text-align: center; white-space: nowrap; }
        .used-by-files { color: var(--blue); font-family: var(--font-mono); white-space: normal; max-width: 60ch; }
		details { cursor: pointer; }
		summary { list-style: none; display: flex; align-items: center; justify-content: space-between; }
		summary::-webkit-details-marker { display: none; }
		summary .item-name { flex-grow: 1; }
		summary .dep-count { padding-left: 1rem; }
		summary::before { content: '▸'; color: var(--cyan); margin-right: 0.5rem; font-size: 0.8em; transition: transform 0.2s; }
		details[open] > summary::before { transform: rotate(90deg); }
		.details-content { padding: 0.75rem 1rem; margin-top: 0.5rem; background-color: var(--bg-color); border-radius: 4px; font-size: 0.9em; }
		.details-content ul { margin: 0; padding-left: 1.2rem; }
		.module-header { color: var(--magenta); margin: 0; padding: 1rem 1.5rem; border-bottom: 1px solid var(--border-color); border-top: 2px solid var(--border-color); }
    </style>
</head>
<body>
    <div class="container">
        <header><h1>✨ Rust Dependency Analysis Report</h1><p>Target Directory: <span class="target-dir">{{ .TargetDir }}</span></p></header>
		<nav>
			<h3>Quick Navigation</h3>
			<div class="nav-links">
				<a href="#top-items">🏆 Top Items</a>
				<a href="#inbound-deps">📥 All Modules</a>
				{{range .AllModules}}<a href="#{{.ID}}">{{.Name}}</a>{{end}}
			</div>
		</nav>
        <main>
			<section class="analysis-section" id="top-items">
				<h2>🏆 Top Imported Items (All Modules)</h2>
				<div class="table-container"><table><thead><tr><th>Item</th><th>From Module</th><th style="text-align: center;">Total Imports</th></tr></thead><tbody>
				{{range .TopImportedItems}}<tr><td class="item-name">{{.Name}}</td><td class="module-name">{{.ModuleName}}</td><td class="dep-count">{{.CountStr}}</td></tr>{{else}}<tr><td colspan="3">No items found.</td></tr>{{end}}
				</tbody></table></div>
			</section>
            <section class="analysis-section" id="inbound-deps">
                <h2>📥 Inbound Module Dependencies</h2>
				<div class="table-container"><table><thead><tr><th>Module</th><th style="text-align: center;">Used by # Files</th><th>Used By Files</th></tr></thead><tbody>
				{{range .AllModules}}<tr><td class="module-name">{{.Name}}</td><td class="dep-count">{{.CountStr}}</td><td class="used-by-files">{{join .Dependents}}</td></tr>{{else}}<tr><td colspan="3">No module dependencies found.</td></tr>{{end}}
				</tbody></table></div>
            </section>
			<section class="analysis-section" id="per-module-analysis">
				<h2 style="border-bottom: none;">📊 Per-Module Item Frequency</h2>
				{{if not .PerModuleItemImports}}<div style="padding: 1.5rem;">No specific item imports found.</div>{{else}}
                    {{range $module, $items := .PerModuleItemImports}}
                    <h3 class="module-header" id="module-{{$module}}">Module: {{$module}}</h3>
					<div class="table-container"><table><thead><tr><th style="width: 100%;">Item & (Click to expand)</th><th style="text-align: center;">Import Count</th></tr></thead><tbody>
					{{range $items}}
					<tr><td colspan="2" style="padding: 0.5rem 1rem;">
						<details>
							<summary><span class="item-name">{{.Name}}</span><span class="dep-count">{{.CountStr}}</span></summary>
							<div class="details-content"><strong>Imported in:</strong><ul>{{range .Files}}<li>{{.}}</li>{{end}}</ul></div>
						</details>
					</td></tr>
					{{end}}
					</tbody></table></div>
                    {{end}}
                {{end}}
			</section>
        </main>
    </div>
</body>
</html>
`