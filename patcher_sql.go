package astipatch

import (
	"bytes"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/molotovtv/go-astilog"
)

// Vars
var (
	sqlQuerySeparator = []byte(";")
)

// patcherSQL represents a SQL patcher
type patcherSQL struct {
	conn         *sqlx.DB
	patches      map[string]*patchSQL // Indexed by name
	patchesNames []string
	storer       Storer
}

// patchSQL represents a SQL patch
type patchSQL struct {
	queries   [][]byte
	rollbacks [][]byte
}

// NewPatcherSQL creates a new SQL patcher
func NewPatcherSQL(conn *sqlx.DB, s Storer) Patcher {
	return &patcherSQL{
		conn:         conn,
		patches:      make(map[string]*patchSQL),
		patchesNames: []string{},
		storer:       s,
	}
}

// Init implements the Patcher interface
func (p *patcherSQL) Init() error {
	return p.storer.Init()
}

// Load loads the patches
func (p *patcherSQL) Load(c Configuration) (err error) {
	astilog.Debug("Loading patches")
	if c.PatchesDirectoryPath != "" {
		astilog.Debugf("Patches directory is %s", c.PatchesDirectoryPath)
		if err = filepath.Walk(c.PatchesDirectoryPath, func(path string, info os.FileInfo, _ error) (err error) {
			// Log
			astilog.Debugf("Processing %s", path)

			// Skip directories
			if info.IsDir() {
				return
			}

			// Skip none .sql files
			if filepath.Ext(path) != ".sql" {
				astilog.Debugf("Skipping non .sql file %s", path)
				return
			}

			// Retrieve name and whether it's a rollback
			var name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			var rollback bool
			if strings.HasSuffix(name, rollbackSuffix) {
				rollback = true
				name = strings.TrimSuffix(name, rollbackSuffix)
			}

			// Read file
			var b []byte
			if b, err = ioutil.ReadFile(path); err != nil {
				return
			}

			// Split on query separator and clean queries
			var items, queries = bytes.Split(b, sqlQuerySeparator), [][]byte{}
			for _, item := range items {
				item = bytes.TrimSpace(item)
				if len(item) > 0 {
					queries = append(queries, item)
				}
			}

			// No queries to add
			if len(queries) == 0 {
				astilog.Debug("No queries to add")
				return
			}

			// Add/update patch
			if _, ok := p.patches[name]; !ok {
				p.patches[name] = &patchSQL{}
			}
			if len(p.patches[name].queries) == 0 && !rollback {
				p.patchesNames = append(p.patchesNames, name)
			}
			if rollback {
				p.patches[name].rollbacks = append(p.patches[name].rollbacks, queries...)
				astilog.Debugf("Adding %d rollback(s) to patch %s", len(queries), name)
			} else {
				p.patches[name].queries = append(p.patches[name].queries, queries...)
				astilog.Debugf("Adding %d querie(s) to patch %s", len(queries), name)
			}
			return
		}); err != nil {
			return
		}
	}
	return
}

// Patch implements the Patcher interface
func (p *patcherSQL) Patch() (err error) {
	// Get patches to run
	var patchesToRun []string
	if patchesToRun, err = p.storer.Delta(p.patchesNames); err != nil {
		return
	}

	// No patches to run
	if len(patchesToRun) == 0 {
		astilog.Debug("No patches to run")
		return
	}

	// Patch
	if err = p.patch(patchesToRun); err != nil {
		return
	}

	// Insert batch
	astilog.Debug("Inserting batch")
	if err = p.storer.InsertBatch(patchesToRun); err != nil {
		return
	}
	return
}

// patch executes a set of query
func (p *patcherSQL) patch(patchesToRun []string) (err error) {
	// Start transaction
	var tx *sqlx.Tx
	if tx, err = p.conn.Beginx(); err != nil {
		return
	}
	astilog.Debug("Beginning transaction")

	// Commit/Rollback
	var rollbacks []string
	defer func(err *error, rollbacks *[]string) {
		if *err != nil {
			// Rollback transaction
			astilog.Debug("Rollbacking transaction")
			if e := tx.Rollback(); e != nil {
				astilog.Errorf("%s while rolling back transaction", e)
			}

			// Run manual rollbacks
			if len(*rollbacks) > 0 {
				astilog.Debug("Running manual rollbacks")
				if e := p.rollback(*rollbacks); e != nil {
					astilog.Errorf("%s while running manual rollbacks", e)
				}
			}
		} else {
			astilog.Debug("Committing transaction")
			if e := tx.Commit(); e != nil {
				astilog.Errorf("%s while committing transaction", e)
			}
		}
	}(&err, &rollbacks)

	// Loop through patches to run
	for _, patch := range patchesToRun {
		// Loop through queries
		for _, query := range p.patches[patch].queries {
			// Exec
			astilog.Debugf("Running query %s of patch %s", string(query), patch)
			if _, err = tx.Exec(string(query)); err != nil {
				astilog.Errorf("%s while executing %s", err, string(query))
				return
			}
		}

		// Add rollbacks in case of errors
		for _, rollback := range p.patches[patch].rollbacks {
			rollbacks = append(rollbacks, string(rollback))
		}
	}
	return
}

// Rollback implements the Patcher interface
func (p *patcherSQL) Rollback() (err error) {
	// Get patches to rollback
	var patchesToRollback []string
	if patchesToRollback, err = p.storer.LastBatch(); err != nil {
		return
	}

	// No patches to rollback
	if len(patchesToRollback) == 0 {
		astilog.Debug("No patches to rollback")
		return
	}

	// Get rollback queries
	var queries []string
	for _, patch := range patchesToRollback {
		for _, rollback := range p.patches[patch].rollbacks {
			queries = append(queries, string(rollback))
		}
	}

	// Rollback
	if err = p.rollback(queries); err != nil {
		return
	}

	// Delete last batch
	astilog.Debug("Deleting last batch")
	if err = p.storer.DeleteLastBatch(); err != nil {
		return
	}
	return
}

// rollback executes a set of query in reverse order
func (p *patcherSQL) rollback(queries []string) (err error) {
	// Start transaction
	var tx *sqlx.Tx
	if tx, err = p.conn.Beginx(); err != nil {
		return
	}
	astilog.Debug("Beginning transaction")

	// Commit/Rollback
	defer func(err *error) {
		if *err != nil {
			astilog.Debug("Rollbacking transaction")
			if e := tx.Rollback(); e != nil {
				astilog.Errorf("%s while rolling back transaction", e)
			}
		} else {
			astilog.Debug("Committing transaction")
			if e := tx.Commit(); e != nil {
				astilog.Errorf("%s while committing transaction", e)
			}
		}
	}(&err)

	// Loop through patches to rollback in reverse order
	for i := len(queries) - 1; i >= 0; i-- {
		astilog.Debugf("Running rollback %s", queries[i])
		if _, err = tx.Exec(queries[i]); err != nil {
			astilog.Errorf("%s while executing %s", err, queries[i])
			return
		}
	}
	return
}
