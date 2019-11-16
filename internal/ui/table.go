package ui

import (
	"context"
	"errors"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/resource"
	"github.com/derailed/tview"
	"github.com/gdamore/tcell"
	"github.com/rs/zerolog/log"
)

type (
	// ColorerFunc represents a row colorer.
	ColorerFunc func(ns string, evt *resource.RowEvent) tcell.Color

	// SelectedRowFunc a table selection callback.
	SelectedRowFunc func(r, c int)
)

// Table represents tabular data.
type Table struct {
	*SelectTable
	KeyActions

	BaseTitle string
	Data      resource.TableData
	cmdBuff   *CmdBuff
	styles    *config.Styles
	colorerFn ColorerFunc
	sortCol   SortColumn
	sortFn    SortFn
}

// NewTable returns a new table view.
func NewTable(title string) *Table {
	return &Table{
		SelectTable: &SelectTable{
			Table: tview.NewTable(),
			marks: make(map[string]bool),
		},
		KeyActions: make(KeyActions),
		cmdBuff:    NewCmdBuff('/', FilterBuff),
		BaseTitle:  title,
		sortCol:    SortColumn{0, 0, true},
	}
}

func (t *Table) Init(ctx context.Context) {
	t.styles = mustExtractSyles(ctx)

	t.SetFixed(1, 0)
	t.SetBorder(true)
	t.SetBackgroundColor(config.AsColor(t.styles.Table().BgColor))
	t.SetBorderColor(config.AsColor(t.styles.Table().FgColor))
	t.SetBorderFocusColor(config.AsColor(t.styles.Frame().Border.FocusColor))
	t.SetBorderAttributes(tcell.AttrBold)
	t.SetBorderPadding(0, 0, 1, 1)
	t.SetSelectable(true, false)
	t.SetSelectedStyle(
		tcell.ColorBlack,
		config.AsColor(t.styles.Table().CursorColor),
		tcell.AttrBold,
	)

	t.SetSelectionChangedFunc(t.selChanged)
	t.SetInputCapture(t.keyboard)
}

// SendKey sends an keyboard event (testing only!).
func (t *Table) SendKey(evt *tcell.EventKey) {
	t.keyboard(evt)
}

func (t *Table) keyboard(evt *tcell.EventKey) *tcell.EventKey {
	key := evt.Key()
	if key == tcell.KeyRune {
		if t.SearchBuff().IsActive() {
			t.SearchBuff().Add(evt.Rune())
			t.ClearSelection()
			t.doUpdate(t.filtered())
			t.UpdateTitle()
			t.SelectFirstRow()
			return nil
		}
		key = asKey(evt)
	}

	if a, ok := t.KeyActions[key]; ok {
		return a.Action(evt)
	}

	return evt
}

// GetFilteredData fetch filtered tabular data.
func (t *Table) GetFilteredData() resource.TableData {
	return t.filtered()
}

// SetColorerFn set the row colorer.
func (t *Table) SetColorerFn(f ColorerFunc) {
	t.colorerFn = f
}

// SetSortCol sets in sort column index and order.
func (t *Table) SetSortCol(index, count int, asc bool) {
	t.sortCol.index, t.sortCol.colCount, t.sortCol.asc = index, count, asc
}

// Update table content.
func (t *Table) Update(data resource.TableData) {
	t.Data = data
	if t.cmdBuff.Empty() {
		t.doUpdate(t.Data)
	} else {
		t.doUpdate(t.filtered())
	}
	t.UpdateTitle()
	t.updateSelection(true)
}

func (t *Table) doUpdate(data resource.TableData) {
	t.ActiveNS = data.Namespace
	if t.ActiveNS == resource.AllNamespaces && t.ActiveNS != "*" {
		t.KeyActions[KeyShiftP] = NewKeyAction("Sort Namespace", t.SortColCmd(-2), false)
	} else {
		t.KeyActions.RmActions(KeyShiftP)
	}
	t.Clear()

	t.adjustSorter(data)

	var row int
	fg := config.AsColor(t.styles.Table().Header.FgColor)
	bg := config.AsColor(t.styles.Table().Header.BgColor)
	for col, h := range data.Header {
		t.AddHeaderCell(data.NumCols[h], col, h)
		c := t.GetCell(0, col)
		c.SetBackgroundColor(bg)
		c.SetTextColor(fg)
	}
	row++

	t.sort(data, row)
}

// SortColCmd designates a sorted column.
func (t *Table) SortColCmd(col int) func(evt *tcell.EventKey) *tcell.EventKey {
	return func(evt *tcell.EventKey) *tcell.EventKey {
		t.sortCol.asc = true
		switch col {
		case -2:
			t.sortCol.index = 0
		case -1:
			t.sortCol.index = t.GetColumnCount() - 1
		default:
			t.sortCol.index = t.NameColIndex() + col

		}
		t.Refresh()

		return nil
	}
}

func (t *Table) adjustSorter(data resource.TableData) {
	// Going from namespace to non namespace or vice-versa?
	switch {
	case t.sortCol.colCount == 0:
	case len(data.Header) > t.sortCol.colCount:
		t.sortCol.index++
	case len(data.Header) < t.sortCol.colCount:
		t.sortCol.index--
	}
	t.sortCol.colCount = len(data.Header)
	if t.sortCol.index < 0 {
		t.sortCol.index = 0
	}
}

func (t *Table) sort(data resource.TableData, row int) {
	pads := make(MaxyPad, len(data.Header))
	ComputeMaxColumns(pads, t.sortCol.index, data)

	sortFn := defaultSort
	if t.sortFn != nil {
		sortFn = t.sortFn
	}
	prim, sec := sortAllRows(t.sortCol, data.Rows, sortFn)
	for _, pk := range prim {
		for _, sk := range sec[pk] {
			t.buildRow(row, data, sk, pads)
			row++
		}
	}

	// check marks if a row is deleted make sure we blow the mark too.
	for k := range t.marks {
		if _, ok := t.Data.Rows[k]; !ok {
			delete(t.marks, k)
		}
	}
}

func (t *Table) buildRow(row int, data resource.TableData, sk string, pads MaxyPad) {
	f := DefaultColorer
	if t.colorerFn != nil {
		f = t.colorerFn
	}
	m := t.IsMarked(sk)
	for col, field := range data.Rows[sk].Fields {
		header := data.Header[col]
		cell, align := formatCell(data.NumCols[header], header, field+Deltas(data.Rows[sk].Deltas[col], field), pads[col])
		c := tview.NewTableCell(cell)
		{
			c.SetExpansion(1)
			c.SetAlign(align)
			c.SetTextColor(f(data.Namespace, data.Rows[sk]))
			if m {
				c.SetBackgroundColor(config.AsColor(t.styles.Table().MarkColor))
			}
		}
		t.SetCell(row, col, c)
	}
}

func (t *Table) ClearMarks() {
	t.marks = map[string]bool{}
	t.Refresh()
}

// Refresh update the table data.
func (t *Table) Refresh() {
	t.Update(t.Data)
}

// NameColIndex returns the index of the resource name column.
func (t *Table) NameColIndex() int {
	col := 0
	if t.ActiveNS == resource.AllNamespaces {
		col++
	}
	return col
}

// AddHeaderCell configures a table cell header.
func (t *Table) AddHeaderCell(numerical bool, col int, name string) {
	c := tview.NewTableCell(sortIndicator(t.sortCol, t.styles.Table(), col, name))
	c.SetExpansion(1)
	if numerical || cpuRX.MatchString(name) || memRX.MatchString(name) {
		c.SetAlign(tview.AlignRight)
	}
	t.SetCell(0, col, c)
}

func (t *Table) filtered() resource.TableData {
	if t.cmdBuff.Empty() || IsLabelSelector(t.cmdBuff.String()) {
		return t.Data
	}

	q := t.cmdBuff.String()
	if isFuzzySelector(q) {
		return fuzzyFilter(q[2:], t.NameColIndex(), t.Data)
	}

	data, err := rxFilter(t.cmdBuff.String(), t.Data)
	if err != nil {
		log.Error().Err(errors.New("Invalid filter expression")).Msg("Regexp")
		t.cmdBuff.Clear()
		return t.Data
	}

	return data
}

// SearchBuff returns the associated command buffer.
func (t *Table) SearchBuff() *CmdBuff {
	return t.cmdBuff
}

// ShowDeleted marks row as deleted.
func (t *Table) ShowDeleted() {
	r, _ := t.GetSelection()
	cols := t.GetColumnCount()
	for x := 0; x < cols; x++ {
		t.GetCell(r, x).SetAttributes(tcell.AttrDim)
	}
}

// UpdateTitle refreshes the table title.
func (t *Table) UpdateTitle() {
	t.SetTitle(styleTitle(t.GetRowCount(), t.ActiveNS, t.BaseTitle, t.cmdBuff.String(), t.styles))
}

// SortInvertCmd reverses sorting order.
func (t *Table) SortInvertCmd(evt *tcell.EventKey) *tcell.EventKey {
	t.sortCol.asc = !t.sortCol.asc
	t.Refresh()

	return nil
}
