// SiYuan - Refactor your thinking
// Copyright (c) 2020-present, b3log.org
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/88250/gulu"
	"github.com/88250/lute/ast"
	"github.com/88250/lute/parse"
	"github.com/jinzhu/copier"
	"github.com/siyuan-note/logging"
	"github.com/siyuan-note/siyuan/kernel/av"
	"github.com/siyuan-note/siyuan/kernel/sql"
	"github.com/siyuan-note/siyuan/kernel/treenode"
	"github.com/siyuan-note/siyuan/kernel/util"
)

func RenderAttributeView(avID string) (viewable av.Viewable, attrView *av.AttributeView, err error) {
	waitForSyncingStorages()

	attrView, err = av.ParseAttributeView(avID)
	if nil != err {
		logging.LogErrorf("parse attribute view [%s] failed: %s", avID, err)
		return
	}

	if 1 > len(attrView.Views) {
		err = av.ErrViewNotFound
		return
	}

	var view *av.View
	if "" != attrView.CurrentViewID {
		for _, v := range attrView.Views {
			if v.ID == attrView.CurrentViewID {
				view = v
				break
			}
		}
	} else {
		view = attrView.Views[0]
	}

	switch view.Type {
	case av.ViewTypeTable:
		viewable, err = renderAttributeViewTable(attrView, view)
	}

	viewable.FilterRows()
	viewable.SortRows()
	viewable.CalcCols()
	return
}

func renderAttributeViewTable(attrView *av.AttributeView, view *av.View) (ret *av.Table, err error) {
	ret = view.Table
	for _, avRow := range attrView.Rows {
		row := &av.TableRow{ID: avRow.ID, Cells: avRow.Cells}
		ret.Rows = append(ret.Rows, row)
	}
	return
}

func (tx *Transaction) doSetAttrViewName(operation *Operation) (ret *TxErr) {
	err := setAttributeViewName(operation)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.AvID, msg: err.Error()}
	}
	return
}

func setAttributeViewName(operation *Operation) (err error) {
	avID := operation.ID
	attrView, err := av.ParseAttributeView(avID)
	if nil != err {
		return
	}

	attrView.Name = operation.Data.(string)

	data, err := gulu.JSON.MarshalJSON(attrView)
	if nil != err {
		return
	}

	if err = gulu.JSON.UnmarshalJSON(data, attrView); nil != err {
		return
	}

	err = av.SaveAttributeView(attrView)
	return
}

func (tx *Transaction) doSetAttrViewFilters(operation *Operation) (ret *TxErr) {
	err := setAttributeViewFilters(operation)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.AvID, msg: err.Error()}
	}
	return
}

func setAttributeViewFilters(operation *Operation) (err error) {
	avID := operation.ID
	attrView, err := av.ParseAttributeView(avID)
	if nil != err {
		return
	}

	view, err := attrView.GetView(operation.ViewID)
	if nil != err {
		return
	}

	operationData := operation.Data.([]interface{})
	data, err := gulu.JSON.MarshalJSON(operationData)
	if nil != err {
		return
	}

	if err = gulu.JSON.UnmarshalJSON(data, &view.Table.Filters); nil != err {
		return
	}

	err = av.SaveAttributeView(attrView)
	return
}

func (tx *Transaction) doSetAttrViewSorts(operation *Operation) (ret *TxErr) {
	err := setAttributeViewSorts(operation)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.AvID, msg: err.Error()}
	}
	return
}

func setAttributeViewSorts(operation *Operation) (err error) {
	avID := operation.ID
	attrView, err := av.ParseAttributeView(avID)
	if nil != err {
		return
	}

	view, err := attrView.GetView(operation.ViewID)
	if nil != err {
		return
	}

	operationData := operation.Data.([]interface{})
	data, err := gulu.JSON.MarshalJSON(operationData)
	if nil != err {
		return
	}

	if err = gulu.JSON.UnmarshalJSON(data, &view.Table.Sorts); nil != err {
		return
	}

	err = av.SaveAttributeView(attrView)
	return
}

func (tx *Transaction) doInsertAttrViewBlock(operation *Operation) (ret *TxErr) {
	firstSrcID := operation.SrcIDs[0]
	tree, err := tx.loadTree(firstSrcID)
	if nil != err {
		logging.LogErrorf("load tree [%s] failed: %s", firstSrcID, err)
		return &TxErr{code: TxErrCodeBlockNotFound, id: firstSrcID, msg: err.Error()}
	}

	for _, id := range operation.SrcIDs {
		var avErr error
		if avErr = addAttributeViewBlock(id, operation, tree, tx); nil != avErr {
			return &TxErr{code: TxErrWriteAttributeView, id: operation.AvID, msg: avErr.Error()}
		}
	}
	return
}

func addAttributeViewBlock(blockID string, operation *Operation, tree *parse.Tree, tx *Transaction) (err error) {
	node := treenode.GetNodeInTree(tree, blockID)
	if nil == node {
		err = ErrBlockNotFound
		return
	}

	if ast.NodeAttributeView == node.Type {
		// 不能将一个属性视图拖拽到另一个属性视图中
		return
	}

	block := sql.BuildBlockFromNode(node, tree)
	if nil == block {
		err = ErrBlockNotFound
		return
	}

	attrView, err := av.ParseAttributeView(operation.AvID)
	if nil != err {
		return
	}

	// 不允许重复添加相同的块到属性视图中
	for _, row := range attrView.Rows {
		blockCell := row.GetBlockCell()
		if nil == blockCell {
			continue
		}

		if blockCell.Value.Block.ID == blockID {
			return
		}
	}

	row := av.NewRow()
	attrs := parse.IAL2Map(node.KramdownIAL)
	for _, col := range attrView.Columns {
		if av.ColumnTypeBlock != col.Type {
			attrs[NodeAttrNamePrefixAvCol+operation.AvID+"-"+col.ID] = "" // 将列作为属性添加到块中
			row.Cells = append(row.Cells, av.NewCell(col.Type))
		} else {
			row.Cells = append(row.Cells, av.NewCellBlock(blockID, getNodeRefText(node)))
		}
	}

	if "" == attrs[NodeAttrNameAVs] {
		attrs[NodeAttrNameAVs] = operation.AvID
	} else {
		avIDs := strings.Split(attrs[NodeAttrNameAVs], ",")
		avIDs = append(avIDs, operation.AvID)
		avIDs = gulu.Str.RemoveDuplicatedElem(avIDs)
		attrs[NodeAttrNameAVs] = strings.Join(avIDs, ",")
	}

	if err = setNodeAttrsWithTx(tx, node, tree, attrs); nil != err {
		return
	}

	if "" == operation.PreviousID {
		attrView.Rows = append([]*av.Row{row}, attrView.Rows...)
	} else {
		for i, r := range attrView.Rows {
			if r.ID == operation.PreviousID {
				attrView.Rows = append(attrView.Rows[:i+1], append([]*av.Row{row}, attrView.Rows[i+1:]...)...)
				break
			}
		}
	}

	err = av.SaveAttributeView(attrView)
	return
}

func (tx *Transaction) doRemoveAttrViewBlock(operation *Operation) (ret *TxErr) {
	for _, id := range operation.SrcIDs {
		var avErr error
		if avErr = removeAttributeViewBlock(id, operation); nil != avErr {
			return &TxErr{code: TxErrWriteAttributeView, id: operation.AvID}
		}
	}
	return
}

func removeAttributeViewBlock(blockID string, operation *Operation) (err error) {
	attrView, err := av.ParseAttributeView(operation.AvID)
	if nil != err {
		return
	}

	for i, row := range attrView.Rows {
		blockCell := row.GetBlockCell()
		if nil == blockCell {
			continue
		}

		if blockCell.Value.Block.ID == blockID {
			// 从行中移除，但是不移除属性
			attrView.Rows = append(attrView.Rows[:i], attrView.Rows[i+1:]...)
			break
		}
	}

	err = av.SaveAttributeView(attrView)
	return
}

func (tx *Transaction) doSetAttrViewColumnWidth(operation *Operation) (ret *TxErr) {
	err := setAttributeViewColWidth(operation)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.ParentID, msg: err.Error()}
	}
	return
}

func setAttributeViewColWidth(operation *Operation) (err error) {
	attrView, err := av.ParseAttributeView(operation.AvID)
	if nil != err {
		return
	}

	view, err := attrView.GetView(operation.ViewID)
	if nil != err {
		return
	}

	for _, column := range view.Table.Columns {
		if column.ID == operation.ID {
			column.Width = operation.Data.(string)
			break
		}
	}

	err = av.SaveAttributeView(attrView)
	return
}

func (tx *Transaction) doSetAttrViewColumnWrap(operation *Operation) (ret *TxErr) {
	err := setAttributeViewColWrap(operation)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.ParentID, msg: err.Error()}
	}
	return
}

func setAttributeViewColWrap(operation *Operation) (err error) {
	attrView, err := av.ParseAttributeView(operation.AvID)
	if nil != err {
		return
	}

	view, err := attrView.GetView(operation.ViewID)
	if nil != err {
		return
	}

	for _, column := range view.Table.Columns {
		if column.ID == operation.ID {
			column.Wrap = operation.Data.(bool)
			break
		}
	}

	err = av.SaveAttributeView(attrView)
	return
}

func (tx *Transaction) doSetAttrViewColumnHidden(operation *Operation) (ret *TxErr) {
	err := setAttributeViewColHidden(operation)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.ParentID, msg: err.Error()}
	}
	return
}

func setAttributeViewColHidden(operation *Operation) (err error) {
	attrView, err := av.ParseAttributeView(operation.AvID)
	if nil != err {
		return
	}

	view, err := attrView.GetView(operation.ViewID)
	if nil != err {
		return
	}

	for _, column := range view.Table.Columns {
		if column.ID == operation.ID {
			column.Hidden = operation.Data.(bool)
			break
		}
	}

	err = av.SaveAttributeView(attrView)
	return
}

func (tx *Transaction) doSortAttrViewRow(operation *Operation) (ret *TxErr) {
	err := sortAttributeViewRow(operation)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.ParentID, msg: err.Error()}
	}
	return
}

func sortAttributeViewRow(operation *Operation) (err error) {
	attrView, err := av.ParseAttributeView(operation.AvID)
	if nil != err {
		return
	}

	var row *av.Row
	var index, previousIndex int
	for i, r := range attrView.Rows {
		if r.ID == operation.ID {
			row = r
			index = i
			break
		}
	}
	if nil == row {
		return
	}

	attrView.Rows = append(attrView.Rows[:index], attrView.Rows[index+1:]...)
	for i, r := range attrView.Rows {
		if r.ID == operation.PreviousID {
			previousIndex = i + 1
			break
		}
	}
	attrView.Rows = util.InsertElem(attrView.Rows, previousIndex, row)

	err = av.SaveAttributeView(attrView)
	return
}

func (tx *Transaction) doSortAttrViewColumn(operation *Operation) (ret *TxErr) {
	err := sortAttributeViewColumn(operation)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.ParentID, msg: err.Error()}
	}
	return
}

func sortAttributeViewColumn(operation *Operation) (err error) {
	attrView, err := av.ParseAttributeView(operation.AvID)
	if nil != err {
		return
	}

	var col *av.Column
	var index, previousIndex int
	for i, column := range attrView.Columns {
		if column.ID == operation.ID {
			col = column
			index = i
			break
		}
	}
	if nil == col {
		return
	}

	attrView.Columns = append(attrView.Columns[:index], attrView.Columns[index+1:]...)
	for i, column := range attrView.Columns {
		if column.ID == operation.PreviousID {
			previousIndex = i + 1
			break
		}
	}
	attrView.Columns = util.InsertElem(attrView.Columns, previousIndex, col)

	for _, row := range attrView.Rows {
		cel := row.Cells[index]
		row.Cells = append(row.Cells[:index], row.Cells[index+1:]...)
		row.Cells = util.InsertElem(row.Cells, previousIndex, cel)
	}

	err = av.SaveAttributeView(attrView)
	return
}

func (tx *Transaction) doAddAttrViewColumn(operation *Operation) (ret *TxErr) {
	err := addAttributeViewColumn(operation)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.ParentID, msg: err.Error()}
	}
	return
}

func addAttributeViewColumn(operation *Operation) (err error) {
	attrView, err := av.ParseAttributeView(operation.AvID)
	if nil != err {
		return
	}

	view, err := attrView.GetView(operation.ViewID)
	if nil != err {
		return
	}

	colType := av.ColumnType(operation.Typ)
	switch colType {
	case av.ColumnTypeText, av.ColumnTypeNumber, av.ColumnTypeDate, av.ColumnTypeSelect, av.ColumnTypeMSelect:
		col := &av.Column{ID: ast.NewNodeID(), Name: operation.Name, Type: colType}
		attrView.Columns = append(attrView.Columns, col)
		view.Table.Columns = append(view.Table.Columns, &av.TableColumn{ID: col.ID, Name: col.Name, Type: col.Type})

		for _, row := range attrView.Rows {
			row.Cells = append(row.Cells, av.NewCell(colType))
		}
	default:
		msg := fmt.Sprintf("invalid column type [%s]", operation.Typ)
		logging.LogErrorf(msg)
		err = errors.New(msg)
		return
	}

	err = av.SaveAttributeView(attrView)
	return
}

func (tx *Transaction) doUpdateAttrViewColumn(operation *Operation) (ret *TxErr) {
	err := updateAttributeViewColumn(operation)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.ParentID, msg: err.Error()}
	}
	return
}

func updateAttributeViewColumn(operation *Operation) (err error) {
	attrView, err := av.ParseAttributeView(operation.AvID)
	if nil != err {
		return
	}

	colType := av.ColumnType(operation.Typ)
	switch colType {
	case av.ColumnTypeText, av.ColumnTypeNumber, av.ColumnTypeDate, av.ColumnTypeSelect, av.ColumnTypeMSelect:
		for _, col := range attrView.Columns {
			if col.ID == operation.ID {
				col.Name = operation.Name
				col.Type = colType
				break
			}
		}

		for _, view := range attrView.Views {
			for _, col := range view.Table.Columns {
				if col.ID == operation.ID {
					col.Name = operation.Name
					col.Type = colType
					break
				}
			}
		}
	default:
		msg := fmt.Sprintf("invalid column type [%s]", operation.Typ)
		logging.LogErrorf(msg)
		err = errors.New(msg)
		return
	}

	err = av.SaveAttributeView(attrView)
	return
}

// TODO 下面的方法要重写

func (tx *Transaction) doUpdateAttrViewCell(operation *Operation) (ret *TxErr) {
	avID := operation.ParentID
	view, err := av.ParseAttributeView(avID)
	if nil != err {
		logging.LogErrorf("parse attribute view [%s] failed: %s", avID, err)
		return &TxErr{code: TxErrCodeBlockNotFound, id: avID, msg: err.Error()}
	}

	var c *av.Cell
	var blockID string
	for _, row := range view.Rows {
		if row.ID != operation.RowID {
			continue
		}

		blockCell := row.GetBlockCell()
		if nil == blockCell {
			continue
		}

		blockID = blockCell.Value.Block.ID
		for _, cell := range row.Cells {
			if cell.ID == operation.ID {
				c = cell
				break
			}
		}
		break
	}

	if nil == c {
		return
	}

	tree, err := tx.loadTree(blockID)
	if nil != err {
		return
	}

	node := treenode.GetNodeInTree(tree, blockID)
	if nil == node {
		return
	}

	data, err := gulu.JSON.MarshalJSON(operation.Data)
	if nil != err {
		return
	}
	if err = gulu.JSON.UnmarshalJSON(data, &c.Value); nil != err {
		return
	}

	attrs := parse.IAL2Map(node.KramdownIAL)
	attrs[NodeAttrNamePrefixAvCol+avID+"-"+c.ID] = c.Value.ToJSONString()
	if err = setNodeAttrsWithTx(tx, node, tree, attrs); nil != err {
		return
	}

	if err = av.SaveAttributeView(view); nil != err {
		return
	}

	return
}

func (tx *Transaction) doUpdateAttrViewColOption(operation *Operation) (ret *TxErr) {
	err := updateAttributeViewColumnOption(operation)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.ParentID, msg: err.Error()}
	}
	return
}

func (tx *Transaction) doRemoveAttrViewColOption(operation *Operation) (ret *TxErr) {
	err := removeAttributeViewColumnOption(operation)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.ParentID, msg: err.Error()}
	}
	return
}

func (tx *Transaction) doUpdateAttrViewColOptions(operation *Operation) (ret *TxErr) {
	err := updateAttributeViewColumnOptions(operation.Data, operation.ID, operation.ParentID)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.ParentID, msg: err.Error()}
	}
	return
}

func (tx *Transaction) doRemoveAttrViewColumn(operation *Operation) (ret *TxErr) {
	err := removeAttributeViewColumn(operation.ID, operation.ParentID)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.ParentID, msg: err.Error()}
	}
	return
}

func (tx *Transaction) doSetAttrView(operation *Operation) (ret *TxErr) {
	err := setAttributeView(operation)
	if nil != err {
		return &TxErr{code: TxErrWriteAttributeView, id: operation.ParentID, msg: err.Error()}
	}
	return
}

func updateAttributeViewColumnOption(operation *Operation) (err error) {
	avID := operation.ParentID
	attrView, err := av.ParseAttributeView(avID)
	if nil != err {
		return
	}

	colID := operation.ID
	data := operation.Data.(map[string]interface{})

	oldName := data["oldName"].(string)
	newName := data["newName"].(string)
	newColor := data["newColor"].(string)

	var colIndex int
	for i, col := range attrView.Columns {
		if col.ID != colID {
			continue
		}

		colIndex = i
		existOpt := false
		for j, opt := range col.Options {
			if opt.Name == newName {
				existOpt = true
				col.Options = append(col.Options[:j], col.Options[j+1:]...)
				break
			}
		}
		if !existOpt {
			for _, opt := range col.Options {
				if opt.Name != oldName {
					continue
				}

				opt.Name = newName
				opt.Color = newColor
				break
			}
		}
		break
	}

	for _, row := range attrView.Rows {
		for i, cell := range row.Cells {
			if colIndex != i || nil == cell.Value {
				continue
			}

			if nil != cell.Value.MSelect && 0 < len(cell.Value.MSelect) && nil != cell.Value.MSelect[0] {
				if oldName == cell.Value.MSelect[0].Content {
					cell.Value.MSelect[0].Content = newName
					cell.Value.MSelect[0].Color = newColor
					break
				}
			} else if nil != cell.Value.MSelect {
				existInMSelect := false
				for j, opt := range cell.Value.MSelect {
					if opt.Content == newName {
						existInMSelect = true
						cell.Value.MSelect = append(cell.Value.MSelect[:j], cell.Value.MSelect[j+1:]...)
						break
					}
				}
				if !existInMSelect {
					for j, opt := range cell.Value.MSelect {
						if oldName == opt.Content {
							cell.Value.MSelect[j].Content = newName
							cell.Value.MSelect[j].Color = newColor
							break
						}
					}
				}
			}
			break
		}
	}

	err = av.SaveAttributeView(attrView)
	return
}

func removeAttributeViewColumnOption(operation *Operation) (err error) {
	avID := operation.ParentID
	attrView, err := av.ParseAttributeView(avID)
	if nil != err {
		return
	}

	colID := operation.ID
	optName := operation.Data.(string)

	var colIndex int
	for i, col := range attrView.Columns {
		if col.ID != colID {
			continue
		}

		colIndex = i

		for j, opt := range col.Options {
			if opt.Name != optName {
				continue
			}

			col.Options = append(col.Options[:j], col.Options[j+1:]...)
			break
		}
		break
	}

	for _, row := range attrView.Rows {
		for i, cell := range row.Cells {
			if colIndex != i {
				continue
			}

			if nil != cell.Value {
				if nil != cell.Value.MSelect && 0 < len(cell.Value.MSelect) && nil != cell.Value.MSelect[0] {
					if optName == cell.Value.MSelect[0].Content {
						cell.Value = nil
						break
					}
				} else if nil != cell.Value.MSelect {
					for j, opt := range cell.Value.MSelect {
						if optName == opt.Content {
							cell.Value.MSelect = append(cell.Value.MSelect[:j], cell.Value.MSelect[j+1:]...)
							break
						}
					}
				}
			}
			break
		}
	}

	err = av.SaveAttributeView(attrView)
	return
}

func updateAttributeViewColumnOptions(data interface{}, id, avID string) (err error) {
	attrView, err := av.ParseAttributeView(avID)
	if nil != err {
		return
	}

	jsonData, err := gulu.JSON.MarshalJSON(data)
	if nil != err {
		return
	}

	options := []*av.ColumnSelectOption{}
	if err = gulu.JSON.UnmarshalJSON(jsonData, &options); nil != err {
		return
	}

	for _, col := range attrView.Columns {
		if col.ID == id {
			col.Options = options
			err = av.SaveAttributeView(attrView)
			return
		}
	}
	return
}

func removeAttributeViewColumn(columnID string, avID string) (err error) {
	attrView, err := av.ParseAttributeView(avID)
	if nil != err {
		return
	}

	for i, column := range attrView.Columns {
		if column.ID == columnID {
			attrView.Columns = append(attrView.Columns[:i], attrView.Columns[i+1:]...)
			for _, row := range attrView.Rows {
				if len(row.Cells) <= i {
					continue
				}

				row.Cells = append(row.Cells[:i], row.Cells[i+1:]...)
			}
			break
		}
	}

	err = av.SaveAttributeView(attrView)
	return
}

func setAttributeView(operation *Operation) (err error) {
	avID := operation.ID
	attrViewMap, err := av.ParseAttributeViewMap(avID)
	if nil != err {
		return
	}

	operationData := operation.Data.(map[string]interface{})
	if err = copier.Copy(&attrViewMap, operationData); nil != err {
		return
	}

	data, err := gulu.JSON.MarshalJSON(attrViewMap)
	if nil != err {
		return
	}

	attrView := &av.AttributeView{}
	if err = gulu.JSON.UnmarshalJSON(data, attrView); nil != err {
		return
	}

	err = av.SaveAttributeView(attrView)
	return
}

const (
	NodeAttrNameAVs         = "custom-avs"
	NodeAttrNamePrefixAvCol = "custom-av-col-"
)
