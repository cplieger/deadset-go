package config

import (
	"bytes"
	"encoding/json"
	"errors"
)

// errNoSingleShape reports a build matrix entry that carries neither shape or both.
// Resolution refuses such an entry, so no resolved configuration holds one and it
// has no document form to be written in.
var errNoSingleShape = errors.New("config: a build matrix entry carries neither shape or both")

// Configuration is one entry of the build matrix, in one of the two shapes the closed
// key list admits: a platform, which is what the Go analysis builds, or a project,
// one compiler configuration file another language's analysis builds. A resolved
// configuration sets exactly one of the two and is written back in that shape.
type Configuration struct {
	Platform *Platform
	Project  *Project
}

// Platform is a build configuration named by an operating system, an architecture
// and the build tags in effect, under the identifier every finding carries.
type Platform struct {
	ID   string   `json:"id"`
	OS   string   `json:"os"`
	Arch string   `json:"arch"`
	Tags []string `json:"tags"`
}

// Project is a build configuration named by one compiler configuration file, as a
// path relative to the target root.
type Project struct {
	ID   string `json:"id"`
	Path string `json:"project"`
}

// entryMembers is one entry of the build matrix as a document writes it: the members
// of both shapes, each a pointer, so the shape an entry takes is read from the
// members it names rather than from their values.
type entryMembers struct {
	ID      *string   `json:"id"`
	OS      *string   `json:"os"`
	Arch    *string   `json:"arch"`
	Tags    *[]string `json:"tags"`
	Project *string   `json:"project"`
}

// Platforms returns the platform entries of the build matrix, in the order the matrix
// lists them, which is the matrix the Go analysis runs. A project entry is the matrix
// of another language's analysis and is not among them, so a matrix listing projects
// alone leaves the Go analysis to derive its own.
func (a *Analysis) Platforms() []Platform {
	var platforms []Platform
	for _, entry := range a.Configurations {
		if entry.Platform != nil {
			platforms = append(platforms, *entry.Platform)
		}
	}
	return platforms
}

// MarshalJSON writes the entry in its own shape: a platform with its tags, written
// as the empty list where it names none, or a project.
func (c *Configuration) MarshalJSON() ([]byte, error) {
	switch {
	case c.Platform != nil && c.Project == nil:
		platform := *c.Platform
		platform.Tags = orEmpty(platform.Tags)
		return json.Marshal(platform)
	case c.Project != nil && c.Platform == nil:
		return json.Marshal(*c.Project)
	default:
		return nil, errNoSingleShape
	}
}

// UnmarshalJSON reads one entry, refusing a member neither shape declares. It sets the
// platform where the entry names a member only a platform declares and the project
// where it names the project, so an entry naming members of both shapes sets both and
// one naming neither sets neither. Resolution refuses either, naming the entry by its
// place in the matrix, which this method does not know.
func (c *Configuration) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var members entryMembers
	if err := dec.Decode(&members); err != nil {
		return err
	}

	*c = Configuration{}
	if members.OS != nil || members.Arch != nil || members.Tags != nil {
		c.Platform = &Platform{
			ID:   valueOf(members.ID),
			OS:   valueOf(members.OS),
			Arch: valueOf(members.Arch),
		}
		if members.Tags != nil {
			c.Platform.Tags = *members.Tags
		}
	}
	if members.Project != nil {
		c.Project = &Project{ID: valueOf(members.ID), Path: *members.Project}
	}
	return nil
}

// valueOf returns the string a member holds, or the empty string where the entry
// does not name the member.
func valueOf(member *string) string {
	if member == nil {
		return ""
	}
	return *member
}
