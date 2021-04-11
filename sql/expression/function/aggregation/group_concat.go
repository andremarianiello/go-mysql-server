// Copyright 2021 Dolthub, Inc.
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

package aggregation

import (
	"fmt"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"sort"
	"strings"
)

type GroupConcat struct {
	distinct string
	sf  sql.SortFields
	separator string
	selectExprs []sql.Expression
}

var _ sql.FunctionExpression = &GroupConcat{}
var _ sql.Aggregation = &GroupConcat{}

func NewEmptyGroupConcat() sql.Expression {
	return &GroupConcat{}
}

func NewGroupConcat(distinct string, orderBy sql.SortFields, separator string, selectExprs []sql.Expression) (*GroupConcat, error) {
	return &GroupConcat{distinct: distinct, sf: orderBy, separator: separator, selectExprs: selectExprs}, nil
}

// NewBuffer creates a new buffer for the aggregation.
func (g *GroupConcat) NewBuffer() sql.Row {
	var rows []sql.Row
	var distinctSet = make(map[string]bool)

	return sql.NewRow(rows, distinctSet)
}

// Update implements the Aggregation interface.
func (g *GroupConcat) Update(ctx *sql.Context, buffer, originalRow sql.Row) error {
	evalRow, err := evalExprs(ctx, g.selectExprs, originalRow)
	if err != nil {
		return err
	}

	// Skip if this is a null row
	if evalRow == nil {
		return nil
	}

	// Get the current value as a string
	v, err := sql.LongText.Convert(evalRow[0])
	if err != nil {
		return err
	}

	if v == nil {
		return nil
	}

	vs := v.(string)

	// Get the current array of rows and the map
	rows := buffer[0].([]sql.Row)
	distinctSet := buffer[1].(map[string]bool)

	// Check if distinct is active if so look at and update our map
	if g.distinct != "" {
		// If this value exists go ahead and return nil
		if _, ok := distinctSet[vs]; ok {
			return nil
		} else {
			distinctSet[vs] = true
		}
	}

	// Append the current value to the end of the row. We want to preserve the row's original structure for
	// for sort ordering in the final step.
	rows = append(rows, append(originalRow, nil, vs))

	buffer[0] = rows
	buffer[1] = distinctSet

	return nil
}

// Merge implements the Aggregation interface.
func (g *GroupConcat) Merge(ctx *sql.Context, buffer, partial sql.Row) error {
	return g.Update(ctx, buffer, partial)
}

// cc: https://dev.mysql.com/doc/refman/8.0/en/aggregate-functions.html#function_group-concat
func (g *GroupConcat) Eval(ctx *sql.Context, row sql.Row) (interface{}, error) {
	rows := row[0].([]sql.Row)
	
	if len(rows) == 0 {
		return nil, nil
	}

	// Execute the order operation if it exists.
	if g.sf != nil {
		sorter := &expression.Sorter{
			SortFields: g.sf,
			Rows: rows,
			Ctx: ctx,
		}

		sort.Stable(sorter)
		if sorter.LastError != nil {
			return nil, sorter.LastError
		}
	}

	ret := ""
	for i, row := range rows {
		lastIdx := len(row) - 1
		if i == len(rows) - 1 {
			ret += row[lastIdx].(string)
		} else {
			ret += row[lastIdx].(string) + g.separator
		}
	}

	maxLen := getGroupConcatMaxLen(ctx)

	if int64(len(ret)) > maxLen {
		ret = ret[0:maxLen]
	}

	return ret, nil
}

func getGroupConcatMaxLen(ctx *sql.Context) int64 {
	_, gcml := ctx.Get("group_concat_max_len")
	return gcml.(int64)
}

func evalExprs(ctx *sql.Context, exprs []sql.Expression, row sql.Row) (sql.Row, error) {
	result := make(sql.Row, len(exprs))
	for i, expr := range exprs {
		var err error
		result[i], err = expr.Eval(ctx, row)
		if err != nil {
			return nil, err
		}
	}

	return result, nil
}

func (g *GroupConcat) Resolved() bool {
	for _, se := range g.selectExprs {
		if !se.Resolved() {
			return false
		}
	}

	return true
}

func (g *GroupConcat) String() string {
	sb := strings.Builder{}
	sb.WriteString("group_concat(")
	if g.distinct != "" {
		sb.WriteString(fmt.Sprintf("distinct %s", g.distinct))
	} else if g.selectExprs != nil {
		var exprs = make([]string, len(g.selectExprs))
		for i, expr := range g.selectExprs {
			exprs[i] = expr.String()
		}

		sb.WriteString(strings.Join(exprs, ", "))
	}

	if len(g.sf) > 0 {
		sb.WriteString(" order by ")
		for i, ob := range g.sf {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(ob.String())
		}
	}

	if g.separator != "," {
		sb.WriteString( " separator ")
		sb.WriteString(fmt.Sprintf("'%s'", g.separator))
	}

	sb.WriteString(")")

	return sb.String()
}

// TODO: Have variable return types for group concat
func (g *GroupConcat) Type() sql.Type {
	return sql.LongText
}

func (g *GroupConcat) IsNullable() bool {
	return false
}

func (g *GroupConcat) Children() []sql.Expression {
	arr := g.sf.ToExpressions()
	return append(arr, g.selectExprs...)
}

func (g *GroupConcat) WithChildren(children ...sql.Expression) (sql.Expression, error) {
	if len(children) == 0 {
		return nil, sql.ErrInvalidChildrenNumber.New(GroupConcat{}, len(children), 2)
	}

	// Get the order by expression using the length of the sort fields.
	delim := len(g.sf)
	orderByExpr := children[:len(g.sf)]

	return NewGroupConcat(g.distinct, g.sf.FromExpressions(orderByExpr), g.separator, children[delim:])
}

func (g *GroupConcat) FunctionName() string {
	return "group_concat"
}