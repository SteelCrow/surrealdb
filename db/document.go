// Copyright © 2016 Abcum Ltd
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

package db

import (
	"time"

	"context"

	"github.com/abcum/surreal/kvs"
	"github.com/abcum/surreal/sql"
	"github.com/abcum/surreal/util/data"
	"github.com/abcum/surreal/util/diff"
	"github.com/abcum/surreal/util/indx"
	"github.com/abcum/surreal/util/keys"
)

type document struct {
	i       *iterator
	ns      string
	db      string
	tb      string
	md      map[string]interface{}
	id      *sql.Thing
	key     *keys.Thing
	val     kvs.KV
	now     int64
	doc     *data.Doc
	initial *data.Doc
	current *data.Doc
	store   struct {
		id int
		tb bool
		ev bool
		fd bool
		ix bool
		ft bool
		lv bool
	}
	cache struct {
		tb *sql.DefineTableStatement
		ev []*sql.DefineEventStatement
		fd []*sql.DefineFieldStatement
		ix []*sql.DefineIndexStatement
		ft []*sql.DefineTableStatement
		lv []*sql.LiveStatement
	}
}

func newDocument(i *iterator, key *keys.Thing, val kvs.KV, doc *data.Doc) (d *document) {

	d = documentPool.Get().(*document)

	d.i = i
	d.id = nil
	d.key = key
	d.val = val
	d.doc = doc

	d.now = time.Now().UnixNano()

	return

}

func (d *document) close() {
	documentPool.Put(d)
}

func (d *document) clear() {
	d.store.tb = false
	d.store.ev = false
	d.store.fd = false
	d.store.ix = false
	d.store.ft = false
	d.store.lv = false
}

func (d *document) erase() (err error) {
	d.current = data.Consume(nil)
	return
}

func (d *document) getTB() (out *sql.DefineTableStatement, err error) {
	if !d.store.tb {
		d.store.tb = true
		d.cache.tb, err = d.i.e.dbo.GetTB(d.key.NS, d.key.DB, d.key.TB)
	}
	return d.cache.tb, err
}

func (d *document) getEV() (out []*sql.DefineEventStatement, err error) {
	if !d.store.ev {
		d.store.ev = true
		d.cache.ev, err = d.i.e.dbo.AllEV(d.key.NS, d.key.DB, d.key.TB)
	}
	return d.cache.ev, err
}

func (d *document) getFD() (out []*sql.DefineFieldStatement, err error) {
	if !d.store.fd {
		d.store.fd = true
		d.cache.fd, err = d.i.e.dbo.AllFD(d.key.NS, d.key.DB, d.key.TB)
	}
	return d.cache.fd, err
}

func (d *document) getIX() (out []*sql.DefineIndexStatement, err error) {
	if !d.store.ix {
		d.store.ix = true
		d.cache.ix, err = d.i.e.dbo.AllIX(d.key.NS, d.key.DB, d.key.TB)
	}
	return d.cache.ix, err
}

func (d *document) getFT() (out []*sql.DefineTableStatement, err error) {
	if !d.store.ft {
		d.store.ft = true
		d.cache.ft, err = d.i.e.dbo.AllFT(d.key.NS, d.key.DB, d.key.TB)
	}
	return d.cache.ft, err
}

func (d *document) getLV() (out []*sql.LiveStatement, err error) {
	if !d.store.lv {
		d.store.lv = true
		d.cache.lv, err = d.i.e.dbo.AllLV(d.key.NS, d.key.DB, d.key.TB)
	}
	return d.cache.lv, err
}

func (d *document) query(ctx context.Context, stm sql.Statement) (interface{}, error) {

	switch stm := stm.(type) {
	default:
		return nil, nil
	case *sql.SelectStatement:
		return d.runSelect(ctx, stm)
	case *sql.CreateStatement:
		return d.runCreate(ctx, stm)
	case *sql.UpdateStatement:
		return d.runUpdate(ctx, stm)
	case *sql.DeleteStatement:
		return d.runDelete(ctx, stm)
	case *sql.RelateStatement:
		return d.runRelate(ctx, stm)
	case *sql.InsertStatement:
		return d.runInsert(ctx, stm)
	case *sql.UpsertStatement:
		return d.runUpsert(ctx, stm)
	}

}

func (d *document) setup() (err error) {

	// A table of records were requested
	// so we have the values, but no key
	// yet, so we need to decode the KV
	// store key into a Thing key.

	if d.key == nil && d.val != nil {
		d.key = &keys.Thing{}
		d.key.Decode(d.val.Key())
	}

	// A specific record has been requested
	// and we have a key, but no value has
	// been loaded yet, so the record needs
	// to be loaded from the KV store.

	if d.key != nil && d.val == nil {
		d.val, err = d.i.e.dbo.Get(d.i.versn, d.key.Encode())
		if err != nil {
			return
		}
	}

	// A subquery or data param has been
	// loaded, and we might not have a key
	// or a value, so let's load the data
	// into a document, so that we can
	// maniuplate the virtual document.

	if d.doc != nil {
		d.initial = d.doc
		d.current = d.doc.Copy()
	}

	// The requested record has been loaded
	// from the KV store (and not from a
	// subquery or data variable), but does
	// not exist. So we'll create a document
	// for processing any record changes.

	if d.doc == nil && d.val != nil && d.val.Exi() == false {
		d.initial = data.New()
		d.current = data.New()
	}

	// The requested record has been loaded
	// from the KV store (and not from a
	// subquery or data variable). So we'll
	// load the KV data into a document for
	// processing any record changes.

	if d.doc == nil && d.val != nil && d.val.Exi() == true {
		d.initial = data.New().Decode(d.val.Val())
		d.current = data.New().Decode(d.val.Val())
	}

	// Finally if we are dealing with a record
	// which is not data from the result of a
	// subquery, then generate the ID from the
	// key and re-calculate any cached data.

	if d.key != nil {

		// Check that the cached data for the
		// current document belongs to the same
		// NS, DB, and TB as the pooled document.
		// If it doesn't then reset the cached data.

		if d.ns != d.key.NS {
			d.ns = d.key.NS
			d.clear()
		}

		if d.db != d.key.DB {
			d.db = d.key.DB
			d.clear()
		}

		if d.tb != d.key.TB {
			d.tb = d.key.TB
			d.clear()
		}

		// Check that the cached data for the
		// current document belongs to the same
		// iterator as the pooled document. If
		// it doesn't then reset the cached data.

		if d.i.id != d.store.id {
			d.store.id = d.i.id
			d.clear()
		}

		// Finally, let's specify the ID of the
		// current document, so we can use it
		// for getting and setting data.

		d.id = sql.NewThing(d.key.TB, d.key.ID)

		d.md = map[string]interface{}{
			"tb": d.key.TB,
			"id": d.key.ID,
		}

	}

	return

}

func (d *document) changed() bool {
	a, _ := d.initial.Data().(map[string]interface{})
	b, _ := d.current.Data().(map[string]interface{})
	c := diff.Diff(a, b)
	return len(c) > 0
}

func (d *document) diff() *data.Doc {
	a, _ := d.initial.Data().(map[string]interface{})
	b, _ := d.current.Data().(map[string]interface{})
	if c := diff.Diff(a, b); len(c) > 0 {
		return data.Consume(c)
	}
	return data.Consume(nil)
}

func (d *document) shouldDrop() (bool, error) {

	// Check whether it is specified
	// that the table should drop
	// writes, and if so, then return.

	tb, err := d.getTB()
	if err != nil {
		return false, err
	}

	return tb.Drop, err

}

func (d *document) storeThing() (err error) {

	// Check that the table should
	// drop data being written.

	if ok, err := d.shouldDrop(); ok {
		return err
	}

	// Check that the rcord has been
	// changed, and if not, return.

	if ok := d.changed(); !ok {
		return
	}

	// Write the value to the data
	// layer and return any errors.

	_, err = d.i.e.dbo.Put(d.now, d.key.Encode(), d.current.Encode())

	return

}

func (d *document) purgeThing() (err error) {

	// Check that the table should
	// drop data being written.

	if ok, err := d.shouldDrop(); ok {
		return err
	}

	// Reset the item by writing a
	// nil value to the storage.

	_, err = d.i.e.dbo.Put(d.now, d.key.Encode(), nil)

	return

}

func (d *document) eraseThing() (err error) {

	// Check that the table should
	// drop data being written.

	if ok, err := d.shouldDrop(); ok {
		return err
	}

	// Delete the item entirely from
	// storage, so no versions exist.

	_, err = d.i.e.dbo.Clr(d.key.Encode())

	return

}

func (d *document) storeIndex() (err error) {

	// Check that the table should
	// drop data being written.

	if ok, err := d.shouldDrop(); ok {
		return err
	}

	// Get the index values specified
	// for this table, loop through
	// them, and compute the changes.

	ixs, err := d.getIX()
	if err != nil {
		return err
	}

	for _, ix := range ixs {

		old := indx.Build(ix.Cols, d.initial)
		now := indx.Build(ix.Cols, d.current)

		indx.Diff(old, now)

		if ix.Uniq == true {
			for _, o := range old {
				oidx := &keys.Index{KV: d.key.KV, NS: d.key.NS, DB: d.key.DB, TB: d.key.TB, IX: ix.Name.ID, FD: o}
				d.i.e.dbo.DelC(d.now, oidx.Encode(), d.id.Bytes())
			}
			for _, n := range now {
				nidx := &keys.Index{KV: d.key.KV, NS: d.key.NS, DB: d.key.DB, TB: d.key.TB, IX: ix.Name.ID, FD: n}
				if _, err = d.i.e.dbo.PutC(0, nidx.Encode(), d.id.Bytes(), nil); err != nil {
					return &IndexError{tb: d.key.TB, name: ix.Name, cols: ix.Cols, vals: n}
				}
			}
		}

		if ix.Uniq == false {
			for _, o := range old {
				oidx := &keys.Point{KV: d.key.KV, NS: d.key.NS, DB: d.key.DB, TB: d.key.TB, IX: ix.Name.ID, FD: o, ID: d.key.ID}
				d.i.e.dbo.DelC(d.now, oidx.Encode(), d.id.Bytes())
			}
			for _, n := range now {
				nidx := &keys.Point{KV: d.key.KV, NS: d.key.NS, DB: d.key.DB, TB: d.key.TB, IX: ix.Name.ID, FD: n, ID: d.key.ID}
				if _, err = d.i.e.dbo.PutC(0, nidx.Encode(), d.id.Bytes(), nil); err != nil {
					return &IndexError{tb: d.key.TB, name: ix.Name, cols: ix.Cols, vals: n}
				}
			}
		}

	}

	return

}

func (d *document) purgeIndex() (err error) {

	// Check that the table should
	// drop data being written.

	if ok, err := d.shouldDrop(); ok {
		return err
	}

	// Get the index values specified
	// for this table, loop through
	// them, and compute the changes.

	ixs, err := d.getIX()
	if err != nil {
		return err
	}

	for _, ix := range ixs {

		old := indx.Build(ix.Cols, d.initial)

		if ix.Uniq == true {
			for _, o := range old {
				key := &keys.Index{KV: d.key.KV, NS: d.key.NS, DB: d.key.DB, TB: d.key.TB, IX: ix.Name.ID, FD: o}
				d.i.e.dbo.DelC(0, key.Encode(), d.id.Bytes())
			}
		}

		if ix.Uniq == false {
			for _, o := range old {
				key := &keys.Point{KV: d.key.KV, NS: d.key.NS, DB: d.key.DB, TB: d.key.TB, IX: ix.Name.ID, FD: o, ID: d.key.ID}
				d.i.e.dbo.DelC(0, key.Encode(), d.id.Bytes())
			}
		}

	}

	return

}
