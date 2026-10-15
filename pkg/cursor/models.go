package cursor

import (
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// Catalog is an account-scoped, immutable snapshot. It contains no credentials.
type Catalog struct {
	ObservedAt time.Time      `json:"observed_at"`
	ExpiresAt  time.Time      `json:"expires_at"`
	Models     []CatalogModel `json:"models"`
}
type ModelParameterValue struct {
	Value         string `json:"value"`
	DisplayName   string `json:"display_name,omitempty"`
	IncreasesCost bool   `json:"increases_cost,omitempty"`
}
type ModelParameter struct {
	ID     string                `json:"id"`
	Name   string                `json:"name"`
	Values []ModelParameterValue `json:"values"`
}
type ModelVariant struct {
	ID         string            `json:"id"`
	Parameters map[string]string `json:"parameters"`
	MaxMode    bool              `json:"max_mode"`
	Default    bool              `json:"-"`
	DefaultMax bool              `json:"-"`
}
type CatalogModel struct {
	ID            string            `json:"id"`
	Provider      string            `json:"provider"`
	BudgetScope   BudgetScope       `json:"budget_scope,omitempty"`
	BaseID        string            `json:"base_id"`
	Parameters    []ModelParameter  `json:"parameters"`
	Variants      []ModelVariant    `json:"variants"`
	Defaults      map[string]string `json:"defaults"`
	DefaultSource string            `json:"default_source"`
	ContextTokens int               `json:"context_tokens,omitempty"`
	ObservedAt    time.Time         `json:"observed_at"`
	Error         string            `json:"error,omitempty"`
}

type catalogPair struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}
type upstreamCatalogModel struct {
	Name           string `json:"name"`
	Vendor         string `json:"vendorName"`
	SupportsAgent  *bool  `json:"supportsAgent"`
	SupportsNonMax *bool  `json:"supportsNonMaxMode"`
	Hidden         bool   `json:"isHidden"`
	ChatOnly       bool   `json:"isChatOnly"`
	CmdOnly        bool   `json:"onlySupportsCmdK"`
	ContextTokens  int    `json:"contextTokenLimit"`
	Definitions    []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type struct {
			Enum *struct {
				Values []struct {
					Value string `json:"value"`
					Name  string `json:"displayName"`
					Cost  bool   `json:"increasesModelCost"`
				} `json:"values"`
			} `json:"enumParameter"`
			Boolean *struct {
				Values []struct {
					Value string `json:"value"`
					Name  string `json:"displayName"`
					Cost  bool   `json:"increasesModelCost"`
				} `json:"values"`
			} `json:"booleanParameter"`
		} `json:"parameterType"`
	} `json:"parameterDefinitions"`
	Variants []struct {
		ID         string        `json:"legacySlug"`
		Parameters []catalogPair `json:"parameterValues"`
		Max        bool          `json:"isMaxMode"`
		Default    bool          `json:"isDefaultNonMaxConfig"`
		DefaultMax bool          `json:"isDefaultMaxConfig"`
	} `json:"variants"`
}

func parseCatalog(picker, usable []byte, now time.Time) (*Catalog, error) {
	var p struct {
		Models []upstreamCatalogModel `json:"models"`
	}
	var u struct {
		Models []struct {
			ID        string `json:"modelId"`
			DisplayID string `json:"displayModelId"`
		} `json:"models"`
	}
	if common.Unmarshal(picker, &p) != nil || common.Unmarshal(usable, &u) != nil || p.Models == nil || u.Models == nil || len(p.Models) > 1024 || len(u.Models) > 4096 {
		return nil, errors.New("invalid Cursor model catalog response")
	}
	ids := map[string]bool{}
	for _, m := range u.Models {
		if m.ID != "" {
			ids[m.ID] = true
		}
		if m.DisplayID != "" {
			ids[m.DisplayID] = true
		}
	}
	result := &Catalog{ObservedAt: now, ExpiresAt: now.Add(5 * time.Minute), Models: []CatalogModel{}}
	seen := map[string]bool{}
	for _, m := range p.Models {
		if m.Name == "" || m.Hidden || m.ChatOnly || m.CmdOnly || (m.SupportsAgent != nil && !*m.SupportsAgent) {
			continue
		}
		if len(m.Definitions) > 32 || len(m.Variants) > 2048 {
			return nil, errors.New("invalid Cursor model catalog limits")
		}
		defs := []ModelParameter{}
		for _, d := range m.Definitions {
			def := ModelParameter{ID: d.ID, Name: d.Name, Values: []ModelParameterValue{}}
			values := d.Type.Enum
			if values == nil {
				values = d.Type.Boolean
			}
			if d.ID == "" || values == nil || len(values.Values) == 0 || len(values.Values) > 128 {
				return nil, errors.New("unsupported Cursor model parameter definition")
			}
			for _, v := range values.Values {
				def.Values = append(def.Values, ModelParameterValue{v.Value, strings.ReplaceAll(v.Name, "\u200b", ""), v.Cost})
			}
			defs = append(defs, def)
		}
		variants := []ModelVariant{}
		for _, v := range m.Variants {
			id := v.ID
			if id == "" {
				id = m.Name
			}
			// Both sources must agree; absence from GetUsableModels is never permission.
			if !ids[id] {
				continue
			}
			params := map[string]string{}
			for _, pair := range v.Parameters {
				params[pair.ID] = pair.Value
			}
			valid := true
			for _, def := range defs {
				value, ok := params[def.ID]
				if !ok || !slices.ContainsFunc(def.Values, func(v ModelParameterValue) bool { return v.Value == value }) {
					valid = false
					break
				}
			}
			if !valid || len(params) != len(defs) {
				continue
			}
			variants = append(variants, ModelVariant{ID: id, Parameters: params, MaxMode: v.Max || (m.SupportsNonMax != nil && !*m.SupportsNonMax), Default: v.Default, DefaultMax: v.DefaultMax})
		}
		if len(m.Variants) == 0 && len(defs) == 0 && ids[m.Name] {
			variants = append(variants, ModelVariant{ID: m.Name, Parameters: map[string]string{}, MaxMode: m.SupportsNonMax != nil && !*m.SupportsNonMax})
		}
		if len(variants) == 0 {
			continue
		}
		provider := m.Vendor
		if provider == "" {
			provider = "cursor"
		}
		names := []string{m.Name}
		for _, v := range variants {
			if !slices.Contains(names, v.ID) {
				names = append(names, v.ID)
			}
		}
		for _, name := range names {
			if seen[name] {
				continue
			}
			selected := variants
			if name != m.Name {
				selected = nil
				for _, v := range variants {
					if v.ID == name {
						selected = append(selected, v)
					}
				}
			}
			entry := CatalogModel{BudgetScope: vendorBudgetScope(m.Vendor), ID: name, BaseID: m.Name, Provider: provider, Variants: selected, Parameters: []ModelParameter{}, ContextTokens: m.ContextTokens, ObservedAt: now}
			for _, def := range defs {
				filtered := ModelParameter{ID: def.ID, Name: def.Name, Values: []ModelParameterValue{}}
				for _, value := range def.Values {
					if slices.ContainsFunc(selected, func(v ModelVariant) bool { return v.Parameters[def.ID] == value.Value }) {
						filtered.Values = append(filtered.Values, value)
					}
				}
				entry.Parameters = append(entry.Parameters, filtered)
			}
			entry.setDefaults()
			result.Models = append(result.Models, entry)
			seen[name] = true
		}
	}
	return result, nil
}

func (m *CatalogModel) setDefaults() {
	m.Defaults = map[string]string{}
	m.DefaultSource = "catalog_order"
	if len(m.Variants) == 0 {
		return
	}
	chosen := m.Variants[0]
	for _, v := range m.Variants {
		if v.DefaultMax {
			chosen = v
			m.DefaultSource = "upstream_max"
			break
		}
	}
	for _, v := range m.Variants {
		if v.Default {
			chosen = v
			m.DefaultSource = "upstream_non_max"
			break
		}
	}
	m.Defaults = maps.Clone(chosen.Parameters)
}

func (c *Catalog) IDs() []string {
	ids := make([]string, 0, len(c.Models))
	for _, m := range c.Models {
		ids = append(ids, m.ID)
	}
	return ids
}
func (c *Catalog) Model(id string) (CatalogModel, bool) {
	for _, m := range c.Models {
		if m.ID == id {
			return m, true
		}
	}
	return CatalogModel{}, false
}

// Intersect limits a routed model to combinations available on every eligible
// Cursor account. The returned value never mutates either cached catalog.
func (m CatalogModel) Intersect(other CatalogModel) CatalogModel {
	if m.BudgetScope != other.BudgetScope {
		m.BudgetScope = ""
	}
	variants := []ModelVariant{}
	if m.Error == "" && other.Error == "" && m.Provider == other.Provider {
		for _, v := range m.Variants {
			if slices.ContainsFunc(other.Variants, func(o ModelVariant) bool {
				return v.ID == o.ID && v.MaxMode == o.MaxMode && maps.Equal(v.Parameters, o.Parameters)
			}) {
				variants = append(variants, v)
			}
		}
	}
	m.Variants = variants
	parameters := []ModelParameter{}
	for _, p := range m.Parameters {
		values := []ModelParameterValue{}
		for _, value := range p.Values {
			if slices.ContainsFunc(variants, func(v ModelVariant) bool { return v.Parameters[p.ID] == value.Value }) {
				values = append(values, value)
			}
		}
		p.Values = values
		parameters = append(parameters, p)
	}
	m.Parameters = parameters
	m.setDefaults()
	if len(variants) == 0 {
		m.Error = "Cursor catalog is unavailable or has no common parameter combination"
	}
	if other.ObservedAt.Before(m.ObservedAt) {
		m.ObservedAt = other.ObservedAt
	}
	return m
}

type modelSelection struct {
	BudgetScope BudgetScope
	ID          string
	Parameters  map[string]string
	MaxMode     bool
}

func resolveModel(catalog *Catalog, r *chatRequest) (modelSelection, error) {
	m, ok := catalog.Model(r.Model)
	if !ok {
		return modelSelection{}, errors.New("model is not available in this Cursor account catalog")
	}
	want := map[string]string{}
	if r.Cursor != nil {
		maps.Copy(want, r.Cursor.Parameters)
	}
	add := func(id, value string) error {
		if prior, ok := want[id]; ok && prior != value {
			return errors.New("conflicting Cursor parameter values")
		}
		want[id] = value
		return nil
	}
	if r.ReasoningEffort != nil {
		id := ""
		for _, candidate := range []string{"reasoning_effort", "effort", "reasoning"} {
			if slices.ContainsFunc(m.Parameters, func(p ModelParameter) bool { return p.ID == candidate }) {
				id = candidate
				break
			}
		}
		if id == "" {
			return modelSelection{}, errors.New("this Cursor model does not advertise reasoning effort")
		}
		if err := add(id, *r.ReasoningEffort); err != nil {
			return modelSelection{}, err
		}
	}
	if r.Cursor != nil {
		if r.Cursor.Fast != nil {
			if err := add("fast", strconv.FormatBool(*r.Cursor.Fast)); err != nil {
				return modelSelection{}, err
			}
		}
		if r.Cursor.Context != nil {
			if err := add("context", *r.Cursor.Context); err != nil {
				return modelSelection{}, err
			}
		}
	}
	for id, value := range want {
		index := slices.IndexFunc(m.Parameters, func(p ModelParameter) bool { return p.ID == id })
		if index < 0 || !slices.ContainsFunc(m.Parameters[index].Values, func(v ModelParameterValue) bool { return v.Value == value }) {
			return modelSelection{}, errors.New("Cursor parameter conflicts with model or account catalog: " + id)
		}
	}
	// Require an exact match for every explicit value; omitted values prefer the
	// upstream default. Never silently reduce effort or change a requested tier.
	best := -1
	score := -1
	for i, v := range m.Variants {
		matches := true
		current := 0
		for id, value := range want {
			if v.Parameters[id] != value {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		for id, value := range m.Defaults {
			if v.Parameters[id] == value {
				current++
			}
		}
		if current > score {
			best = i
			score = current
		}
	}
	if best < 0 {
		return modelSelection{}, errors.New("Cursor parameter combination is not available for this model and account")
	}
	v := m.Variants[best]
	return modelSelection{BudgetScope: m.BudgetScope, ID: v.ID, Parameters: maps.Clone(v.Parameters), MaxMode: v.MaxMode}, nil
}
