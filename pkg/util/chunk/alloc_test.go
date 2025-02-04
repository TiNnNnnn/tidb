// Copyright 2021 PingCAP, Inc.
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

package chunk

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/pingcap/tidb/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestAllocator(t *testing.T) {
	alloc := NewAllocator()

	fieldTypes := []*types.FieldType{
		types.NewFieldType(mysql.TypeVarchar),
		types.NewFieldType(mysql.TypeJSON),
		types.NewFieldType(mysql.TypeFloat),
		types.NewFieldType(mysql.TypeNewDecimal),
		types.NewFieldType(mysql.TypeDouble),
		types.NewFieldType(mysql.TypeLonglong),
		types.NewFieldType(mysql.TypeTimestamp),
		types.NewFieldType(mysql.TypeDatetime),
	}

	initCap := 5
	maxChunkSize := 100

	chk := alloc.Alloc(fieldTypes, initCap, maxChunkSize)
	require.NotNil(t, chk)
	check := func() {
		require.Equal(t, len(fieldTypes), chk.NumCols())
		require.Nil(t, chk.columns[0].elemBuf)
		require.Nil(t, chk.columns[1].elemBuf)
		require.Equal(t, getFixedLen(fieldTypes[2]), len(chk.columns[2].elemBuf))
		require.Equal(t, getFixedLen(fieldTypes[3]), len(chk.columns[3].elemBuf))
		require.Equal(t, getFixedLen(fieldTypes[4]), len(chk.columns[4].elemBuf))
		require.Equal(t, getFixedLen(fieldTypes[5]), len(chk.columns[5].elemBuf))
		require.Equal(t, getFixedLen(fieldTypes[6]), len(chk.columns[6].elemBuf))
		require.Equal(t, getFixedLen(fieldTypes[7]), len(chk.columns[7].elemBuf))

		require.Equal(t, initCap*getFixedLen(fieldTypes[2]), cap(chk.columns[2].data))
		require.Equal(t, initCap*getFixedLen(fieldTypes[3]), cap(chk.columns[3].data))
		require.Equal(t, initCap*getFixedLen(fieldTypes[4]), cap(chk.columns[4].data))
		require.Equal(t, initCap*getFixedLen(fieldTypes[5]), cap(chk.columns[5].data))
		require.Equal(t, initCap*getFixedLen(fieldTypes[6]), cap(chk.columns[6].data))
		require.Equal(t, initCap*getFixedLen(fieldTypes[7]), cap(chk.columns[7].data))
	}
	check()

	// Call Reset and alloc again, check the result.
	alloc.Reset()
	chk = alloc.Alloc(fieldTypes, initCap, maxChunkSize)
	check()

	// Check maxFreeListLen
	for range maxFreeChunks + 10 {
		alloc.Alloc(fieldTypes, initCap, maxChunkSize)
	}
	alloc.Reset()
	require.Equal(t, len(alloc.free), maxFreeChunks)
}

func TestColumnAllocator(t *testing.T) {
	fieldTypes := []*types.FieldType{
		types.NewFieldType(mysql.TypeVarchar),
		types.NewFieldType(mysql.TypeJSON),
		types.NewFieldType(mysql.TypeFloat),
		types.NewFieldType(mysql.TypeNewDecimal),
		types.NewFieldType(mysql.TypeDouble),
		types.NewFieldType(mysql.TypeLonglong),
		types.NewFieldType(mysql.TypeTimestamp),
		types.NewFieldType(mysql.TypeDatetime),
	}

	var alloc1 poolColumnAllocator
	alloc1.init()
	var alloc2 DefaultColumnAllocator

	// Test the basic allocate operation.
	initCap := 5
	for _, ft := range fieldTypes {
		v0 := NewColumn(ft, initCap)
		v1 := alloc1.NewColumn(ft, initCap)
		v2 := alloc2.NewColumn(ft, initCap)
		require.Equal(t, v0, v1)
		require.Equal(t, v1, v2)
	}

	ft := fieldTypes[2]
	// Test reuse.
	cols := make([]*Column, 0, maxFreeColumnsPerType+10)
	for range maxFreeColumnsPerType + 10 {
		col := alloc1.NewColumn(ft, 20)
		cols = append(cols, col)
	}
	for _, col := range cols {
		alloc1.put(col)
	}

	// Check max column size.
	freeList := alloc1.pool[getFixedLen(ft)]
	require.NotNil(t, freeList)
	require.Equal(t, freeList.Len(), maxFreeColumnsPerType)
}

func TestNoDuplicateColumnReuse(t *testing.T) {
	// For issue https://github.com/pingcap/tidb/issues/29554
	// Some chunk columns are just references to other chunk columns.
	// So when reusing Chunk, some columns may point to the same memory address.

	fieldTypes := []*types.FieldType{
		types.NewFieldType(mysql.TypeVarchar),
		types.NewFieldType(mysql.TypeJSON),
		types.NewFieldType(mysql.TypeFloat),
		types.NewFieldType(mysql.TypeNewDecimal),
		types.NewFieldType(mysql.TypeDouble),
		types.NewFieldType(mysql.TypeLonglong),
		types.NewFieldType(mysql.TypeTimestamp),
		types.NewFieldType(mysql.TypeDatetime),
	}
	alloc := NewAllocator()
	for range maxFreeChunks + 10 {
		chk := alloc.Alloc(fieldTypes, 5, 10)
		chk.MakeRef(1, 3)
	}
	alloc.Reset()

	a := alloc.columnAlloc
	// Make sure no duplicated column in the pool.
	for _, p := range a.pool {
		dup := make(map[*Column]struct{})
		for !p.empty() {
			c := p.pop()
			_, exist := dup[c]
			require.False(t, exist)
			dup[c] = struct{}{}
		}
	}
}

func TestAvoidColumnReuse(t *testing.T) {
	// For issue: https://github.com/pingcap/tidb/issues/31981
	// Some chunk columns are references to rpc message.
	// So when reusing Chunk, we should ignore them.

	fieldTypes := []*types.FieldType{
		types.NewFieldTypeBuilder().SetType(mysql.TypeVarchar).BuildP(),
		types.NewFieldTypeBuilder().SetType(mysql.TypeJSON).BuildP(),
		types.NewFieldTypeBuilder().SetType(mysql.TypeFloat).BuildP(),
		types.NewFieldTypeBuilder().SetType(mysql.TypeNewDecimal).BuildP(),
		types.NewFieldTypeBuilder().SetType(mysql.TypeDouble).BuildP(),
		types.NewFieldTypeBuilder().SetType(mysql.TypeLonglong).BuildP(),
		types.NewFieldTypeBuilder().SetType(mysql.TypeTimestamp).BuildP(),
		types.NewFieldTypeBuilder().SetType(mysql.TypeDatetime).BuildP(),
	}
	alloc := NewAllocator()
	for range maxFreeChunks + 10 {
		chk := alloc.Alloc(fieldTypes, 5, 10)
		for _, col := range chk.columns {
			col.avoidReusing = true
		}
	}
	alloc.Reset()

	a := alloc.columnAlloc
	// Make sure no duplicated column in the pool.
	for _, p := range a.pool {
		require.True(t, p.empty())
	}

	// test decoder will set avoid reusing flag.
	chk := alloc.Alloc(fieldTypes, 5, 1024)
	for range 10 {
		for _, col := range chk.columns {
			col.AppendNull()
		}
	}
	codec := &Codec{fieldTypes}
	buf := codec.Encode(chk)

	decoder := NewDecoder(
		NewChunkWithCapacity(fieldTypes, 0),
		fieldTypes,
	)
	decoder.Reset(buf)
	decoder.ReuseIntermChk(chk)
	for _, col := range chk.columns {
		require.True(t, col.avoidReusing)
	}
}

func TestColumnAllocatorLimit(t *testing.T) {
	fieldTypes := []*types.FieldType{
		types.NewFieldTypeBuilder().SetType(mysql.TypeVarchar).BuildP(),
		types.NewFieldTypeBuilder().SetType(mysql.TypeJSON).BuildP(),
		types.NewFieldTypeBuilder().SetType(mysql.TypeFloat).BuildP(),
		types.NewFieldTypeBuilder().SetType(mysql.TypeNewDecimal).BuildP(),
		types.NewFieldTypeBuilder().SetType(mysql.TypeDouble).BuildP(),
		types.NewFieldTypeBuilder().SetType(mysql.TypeLonglong).BuildP(),
		types.NewFieldTypeBuilder().SetType(mysql.TypeDatetime).BuildP(),
	}

	//set cache size
	InitChunkAllocSize(10, 20)
	alloc := NewAllocator()
	require.True(t, alloc.CheckReuseAllocSize())
	for range maxFreeChunks + 10 {
		alloc.Alloc(fieldTypes, 5, 10)
	}
	alloc.Reset()
	require.Equal(t, len(alloc.free), 10)
	for _, p := range alloc.columnAlloc.pool {
		require.True(t, (p.Len() <= 20))
	}

	//Reduce capacity
	InitChunkAllocSize(5, 10)
	alloc = NewAllocator()
	for range maxFreeChunks + 10 {
		alloc.Alloc(fieldTypes, 5, 10)
	}
	alloc.Reset()
	require.Equal(t, len(alloc.free), 5)
	for _, p := range alloc.columnAlloc.pool {
		require.True(t, (p.Len() <= 10))
	}

	//increase capacity
	InitChunkAllocSize(50, 100)
	alloc = NewAllocator()
	for range maxFreeChunks + 10 {
		alloc.Alloc(fieldTypes, 5, 10)
	}
	alloc.Reset()
	require.Equal(t, len(alloc.free), 50)
	for _, p := range alloc.columnAlloc.pool {
		require.True(t, (p.Len() <= 100))
	}

	//long characters are not cached
	alloc = NewAllocator()
	rs := alloc.Alloc([]*types.FieldType{types.NewFieldTypeBuilder().SetType(mysql.TypeVarchar).BuildP()}, 1024, 1024)
	nu := len(alloc.columnAlloc.pool[VarElemLen].allocColumns)
	require.Equal(t, nu, 1)
	for _, col := range rs.columns {
		for range 20480 {
			col.data = append(col.data, byte('a'))
		}
	}
	alloc.Reset()
	for _, p := range alloc.columnAlloc.pool {
		require.True(t, (p.Len() == 0))
	}

	InitChunkAllocSize(0, 0)
	alloc = NewAllocator()
	require.False(t, alloc.CheckReuseAllocSize())
}

func TestColumnAllocatorCheck(t *testing.T) {
	fieldTypes := []*types.FieldType{
		types.NewFieldTypeBuilder().SetType(mysql.TypeFloat).BuildP(),
		types.NewFieldTypeBuilder().SetType(mysql.TypeDatetime).BuildP(),
	}
	InitChunkAllocSize(10, 20)
	alloc := NewAllocator()
	for range 4 {
		alloc.Alloc(fieldTypes, 5, 10)
	}
	col := alloc.columnAlloc.NewColumn(types.NewFieldTypeBuilder().SetType(mysql.TypeFloat).BuildP(), 10)
	col.Reset(types.ETDatetime)
	alloc.Reset()
	num := alloc.columnAlloc.pool[getFixedLen(types.NewFieldTypeBuilder().SetType(mysql.TypeFloat).BuildP())].Len()
	require.Equal(t, num, 4)
	num = alloc.columnAlloc.pool[getFixedLen(types.NewFieldTypeBuilder().SetType(mysql.TypeDatetime).BuildP())].Len()
	require.Equal(t, num, 4)
}

func TestReuseHookAllocator(t *testing.T) {
	fieldTypes := []*types.FieldType{
		types.NewFieldType(mysql.TypeVarchar),
		types.NewFieldType(mysql.TypeJSON),
		types.NewFieldType(mysql.TypeFloat),
		types.NewFieldType(mysql.TypeNewDecimal),
		types.NewFieldType(mysql.TypeDouble),
		types.NewFieldType(mysql.TypeLonglong),
		types.NewFieldType(mysql.TypeTimestamp),
		types.NewFieldType(mysql.TypeDatetime),
	}

	var reuse atomic.Int64

	InitChunkAllocSize(0, 0)
	alloc := NewReuseHookAllocator(NewAllocator(), func() {
		reuse.Add(1)
	})
	// as we init MaxFreeChunks and MaxFreeColumns as 0, the reuse is still 0 after alloc
	chk := alloc.Alloc(fieldTypes, 5, 100)
	require.NotNil(t, chk)
	require.Equal(t, int64(0), reuse.Load())

	InitChunkAllocSize(10, 20)
	alloc = NewReuseHookAllocator(NewAllocator(), func() {
		reuse.Add(1)
	})
	chk = alloc.Alloc(fieldTypes, 5, 100)
	require.NotNil(t, chk)
	require.Equal(t, int64(1), reuse.Load())
	// Another alloc will not touch it
	chk = alloc.Alloc(fieldTypes, 5, 100)
	require.NotNil(t, chk)
	require.Equal(t, int64(1), reuse.Load())
}

func TestSyncAllocator(t *testing.T) {
	fieldTypes := []*types.FieldType{
		types.NewFieldType(mysql.TypeVarchar),
		types.NewFieldType(mysql.TypeJSON),
		types.NewFieldType(mysql.TypeFloat),
		types.NewFieldType(mysql.TypeNewDecimal),
		types.NewFieldType(mysql.TypeDouble),
		types.NewFieldType(mysql.TypeLonglong),
		types.NewFieldType(mysql.TypeTimestamp),
		types.NewFieldType(mysql.TypeDatetime),
	}

	alloc := NewSyncAllocator(NewAllocator())

	wg := &sync.WaitGroup{}
	for range 1000 {
		wg.Add(1)
		go func() {
			for range 10 {
				for range 100 {
					chk := alloc.Alloc(fieldTypes, 5, 100)
					require.NotNil(t, chk)
				}
				alloc.Reset()
			}

			wg.Done()
		}()
	}
	wg.Wait()
}
