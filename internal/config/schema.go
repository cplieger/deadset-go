package config

import (
	"bytes"
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// severityKeyPattern is the spelling a severity key takes: a code, or a
// two-digit range prefix naming a whole family.
//
//nolint:gocritic // regexpSimplify: the pattern is the schema's own spelling, pinned equal to it
var severityKeyPattern = regexp.MustCompile(`^DS[0-9]{2}([0-9]{2})?$`)

// exemptionClassPattern is the spelling an exemption class name takes.
var exemptionClassPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// contractVersionPattern is the spelling contract_version takes.
//
//nolint:gocritic // regexpSimplify: the pattern is the schema's own spelling, pinned equal to it
var contractVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// fixedSeverityCodes are the codes whose severity the Contract fixes. A severity
// key naming one, or a family prefix whose range holds one, is an unimplemented
// key rather than a setting.
func fixedSeverityCodes() []string { return []string{"DS1703"} }

// keyKind classifies one node of the closed key list.
type keyKind uint8

const (
	// keyLeaf is a setting: a scalar, or an array of scalars.
	keyLeaf keyKind = iota
	// keySection is an object whose member names the key list declares.
	keySection
	// keyMap is an object whose member names the key list leaves open, its
	// values scalars: the severity and provenance objects.
	keyMap
	// keyList is an array whose entries are objects with declared members.
	keyList
)

// keyNode is one node of the closed key list. members holds a section's declared
// members, or a list entry's, and is empty otherwise.
type keyNode struct {
	members map[string]keyNode
	kind    keyKind
}

// schemaRoot returns the closed key list the Contract's configuration schema
// declares. It is derived from that schema key by key, and a test pins the two
// equal, so a Contract change that adds a key fails here rather than being
// silently unimplemented.
func schemaRoot() keyNode {
	return keyNode{kind: keySection, members: map[string]keyNode{
		"contract_version": {kind: keyLeaf},
		"target": {kind: keySection, members: map[string]keyNode{
			"kind": {kind: keyLeaf},
		}},
		"analysis": {kind: keySection, members: map[string]keyNode{
			"languages":       {kind: keyLeaf},
			"min_confidence":  {kind: keyLeaf},
			"generated_files": {kind: keyLeaf},
			"consumer_tests":  {kind: keyLeaf},
			"configurations": {kind: keyList, members: map[string]keyNode{
				"id":   {kind: keyLeaf},
				"os":   {kind: keyLeaf},
				"arch": {kind: keyLeaf},
				"tags": {kind: keyLeaf},
			}},
			"matrix": {kind: keySection, members: map[string]keyNode{
				"complete": {kind: keyLeaf},
			}},
			"template_dirs": {kind: keyLeaf},
		}},
		"consumers": {kind: keySection, members: map[string]keyNode{
			"complete": {kind: keyLeaf},
		}},
		"roots": {kind: keySection, members: map[string]keyNode{
			"patterns": {kind: keyLeaf},
		}},
		"severity": {kind: keyMap},
		"exemptions": {kind: keySection, members: map[string]keyNode{
			"disabled": {kind: keyLeaf},
		}},
		"reporters": {kind: keySection, members: map[string]keyNode{
			"formats":      {kind: keyLeaf},
			"sort":         {kind: keyLeaf},
			"max_findings": {kind: keyLeaf},
			"fail_on":      {kind: keyLeaf},
		}},
		"go": {kind: keySection, members: map[string]keyNode{}},
		"ts": {kind: keySection, members: map[string]keyNode{
			"test_files":  {kind: keyLeaf},
			"entry_files": {kind: keyLeaf},
		}},
		"provenance": {kind: keyMap},
	}}
}

// member returns the node one member name resolves to. A member of an open
// object, and any member below a setting, resolves to a leaf, so the walk
// descends far enough to find a repeated member at any depth.
func (n keyNode) member(name string) (keyNode, bool) {
	if n.kind == keySection || n.kind == keyList {
		child, declared := n.members[name]
		return child, declared
	}
	return keyNode{kind: keyLeaf}, true
}

// declaredKeys returns every key the closed key list declares, as a dotted path,
// in ascending order: the sections, the settings, and the two open objects.
func declaredKeys() []string {
	var keys []string
	collectKeys(schemaRoot(), "", &keys)
	slices.Sort(keys)
	return keys
}

// collectKeys appends the dotted path of every key below node. A list's entry
// members are spelled under the list's own path followed by empty brackets, the
// way a refusal names the member of one entry.
func collectKeys(node keyNode, at string, keys *[]string) {
	for name, child := range node.members {
		path := joinKey(at, name)
		*keys = append(*keys, path)
		if child.kind == keyList {
			collectKeys(child, path+"[]", keys)
			continue
		}
		collectKeys(child, path, keys)
	}
}

// settingPaths returns the dotted path of every setting that holds one value, in
// ascending order. A list is one setting, its entries not addressable on their
// own, and the severity object is not among them: it resolves per code.
func settingPaths() []string {
	var paths []string
	collectSettings(schemaRoot(), "", &paths)
	slices.Sort(paths)
	return paths
}

// collectSettings appends the dotted path of every setting below node.
func collectSettings(node keyNode, at string, paths *[]string) {
	for name, child := range node.members {
		path := joinKey(at, name)
		if child.kind == keyLeaf || child.kind == keyList {
			*paths = append(*paths, path)
			continue
		}
		collectSettings(child, path, paths)
	}
}

// joinKey spells the dotted path of one member of the value at at.
func joinKey(at, name string) string {
	if at == "" {
		return name
	}
	return at + "." + name
}

// nearestKey returns the implemented key closest to key: the one sharing the most
// leading dotted segments, and among those the smallest edit distance over the
// whole path, ties broken in ascending order.
func nearestKey(key string) string {
	segments := strings.Split(key, ".")
	nearest, bestShared, bestDistance := "", -1, 0
	for _, candidate := range declaredKeys() {
		shared := sharedSegments(segments, strings.Split(candidate, "."))
		distance := editDistance(key, candidate)
		if shared < bestShared || (shared == bestShared && distance >= bestDistance) {
			continue
		}
		nearest, bestShared, bestDistance = candidate, shared, distance
	}
	return nearest
}

// sharedSegments counts the leading dotted segments two paths hold in common.
func sharedSegments(a, b []string) int {
	shared := 0
	for shared < len(a) && shared < len(b) && a[shared] == b[shared] {
		shared++
	}
	return shared
}

// editDistance returns the Levenshtein distance between two strings, counted in
// runes so a key carrying non-ASCII text is measured by character.
func editDistance(a, b string) int {
	from, to := []rune(a), []rune(b)
	previous := make([]int, len(to)+1)
	current := make([]int, len(to)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(from); i++ {
		current[0] = i
		for j := 1; j <= len(to); j++ {
			substitution := previous[j-1]
			if from[i-1] != to[j-1] {
				substitution++
			}
			current[j] = min(substitution, previous[j]+1, current[j-1]+1)
		}
		previous, current = current, previous
	}
	return previous[len(to)]
}

// checkDocument refuses a member the closed key list does not declare, naming its
// dotted path, and a member one object writes twice, naming the same. A decoder
// unmarshalling into a struct cannot report the second: the later value replaces
// the earlier one, so the document reads as though it named the member once.
func checkDocument(data []byte, label string) *Error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := walkValue(dec, "", schemaRoot(), label); err != nil {
		return err
	}
	if dec.More() {
		return malformed(label, "", "want one JSON object, a second value follows it")
	}
	return nil
}

// walkValue reads the one value at the decoder's position, checking it and its
// descendants against node. at is the dotted path of that value.
func walkValue(dec *json.Decoder, at string, node keyNode, label string) *Error {
	token, err := dec.Token()
	if err != nil {
		return malformed(label, at, "%s", err)
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		return walkObject(dec, at, node, label)
	case '[':
		return walkArray(dec, at, node, label)
	default:
		return malformed(label, at, "unexpected %q", delim)
	}
}

// walkObject reads the members of the object the decoder has just opened.
func walkObject(dec *json.Decoder, at string, node keyNode, label string) *Error {
	seen := make(map[string]bool)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return malformed(label, at, "%s", err)
		}
		name, isName := token.(string)
		if !isName {
			return malformed(label, at, "want a member name, got %v", token)
		}
		path := joinKey(at, name)
		if seen[name] {
			return malformed(label, path, "member %q is written twice, so neither value is chosen", name)
		}
		seen[name] = true
		child, declared := node.member(name)
		if !declared {
			return unimplementedKey(label, path)
		}
		if err := walkValue(dec, path, child, label); err != nil {
			return err
		}
	}
	_, err := dec.Token()
	if err != nil {
		return malformed(label, at, "%s", err)
	}
	return nil
}

// walkArray reads the entries of the array the decoder has just opened. A list's
// entries carry the members the key list declares for them; every other array
// holds scalars.
func walkArray(dec *json.Decoder, at string, node keyNode, label string) *Error {
	entry := keyNode{kind: keyLeaf}
	if node.kind == keyList {
		entry = keyNode{kind: keySection, members: node.members}
	}
	for index := 0; dec.More(); index++ {
		path := at + "[" + strconv.Itoa(index) + "]"
		if err := walkValue(dec, path, entry, label); err != nil {
			return err
		}
	}
	_, err := dec.Token()
	if err != nil {
		return malformed(label, at, "%s", err)
	}
	return nil
}

// nestFlags rewrites the flag document, whose keys are dotted setting paths, as
// the nested object the closed key list declares, so one decoder reads every
// source. A key naming no setting is refused.
func nestFlags(data []byte, label string) ([]byte, *Error) {
	if err := checkFlagDuplicates(data, label); err != nil {
		return nil, err
	}
	var flat map[string]json.RawMessage
	if err := json.Unmarshal(data, &flat); err != nil {
		return nil, malformed(label, "", "%s", err)
	}
	nested := make(map[string]any, len(flat))
	for _, path := range slices.Sorted(maps.Keys(flat)) {
		if !isFlagSetting(path) {
			return nil, unimplementedKey(label, path)
		}
		if err := nest(nested, strings.Split(path, "."), flat[path], label); err != nil {
			return nil, err
		}
	}
	document, err := json.Marshal(nested)
	if err != nil {
		return nil, malformed(label, "", "%s", err)
	}
	return document, nil
}

// checkFlagDuplicates refuses a flag document that names one path twice.
func checkFlagDuplicates(data []byte, label string) *Error {
	dec := json.NewDecoder(bytes.NewReader(data))
	return walkValue(dec, "", keyNode{kind: keyMap}, label)
}

// isFlagSetting reports whether one dotted path names a setting a flag supplies:
// a setting of the closed key list, or one code of the severity object.
func isFlagSetting(path string) bool {
	if slices.Contains(settingPaths(), path) {
		return true
	}
	section, code, found := strings.Cut(path, ".")
	return found && section == "severity" && code != "" && !strings.Contains(code, ".")
}

// nest writes one value into the nested document at the path its segments name.
func nest(into map[string]any, segments []string, value json.RawMessage, label string) *Error {
	path := strings.Join(segments, ".")
	for _, segment := range segments[:len(segments)-1] {
		child, held := into[segment]
		if !held {
			child = map[string]any{}
			into[segment] = child
		}
		section, isSection := child.(map[string]any)
		if !isSection {
			return malformed(label, path, "the flag document sets %q and a setting below it", segment)
		}
		into = section
	}
	last := segments[len(segments)-1]
	if _, held := into[last]; held {
		return malformed(label, path, "the flag document sets %q twice", path)
	}
	into[last] = value
	return nil
}
