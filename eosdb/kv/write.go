// Copyright 2019 dfuse Platform Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kv

import (
	"context"
	"fmt"
	"math"

	pbdeos "github.com/dfuse-io/pbgo/dfuse/codecs/deos"
	"github.com/dfuse-io/bstream"
	"github.com/dfuse-io/bstream/codecs/deos"
	"github.com/dfuse-io/kvdb/eosdb"
	pbrows "github.com/dfuse-io/kvdb/eosdb/kv/pb"
	"github.com/golang/protobuf/ptypes"
	"go.uber.org/zap"
)

func (db *DB) Flush(ctx context.Context) error {
	return db.store.FlushPuts(ctx)
}

func (db *DB) SetWriterChainID(chainID []byte) {
	db.writerChainID = chainID
}

func (db *DB) GetLastWrittenIrreversibleBlockRef(ctx context.Context) (ref bstream.BlockRef, err error) {
	return db.GetClosestIrreversibleIDAtBlockNum(ctx, math.MaxUint32)
}

func (db *DB) PutBlock(ctx context.Context, blk *pbdeos.Block) error {
	if err := db.putTransactions(ctx, blk); err != nil {
		return fmt.Errorf("put block: unable to putTransactions: %w", err)
	}

	if err := db.putTransactionTraces(ctx, blk); err != nil {
		return fmt.Errorf("put block: unable to putTransactions: %w", err)
	}

	if err := db.putImplicitTransactions(ctx, blk); err != nil {
		return fmt.Errorf("put block: unable to putTransactions: %w", err)
	}

	return db.putBlock(ctx, blk)
}

func (db *DB) putTransactions(ctx context.Context, blk *pbdeos.Block) error {
	for _, trxReceipt := range blk.Transactions {
		if trxReceipt.PackedTransaction == nil {
			// This means we deal with a deferred transaction receipt, and that it has been handled through DtrxOps already
			continue
		}

		signedTransaction, err := deos.ExtractEOSSignedTransactionFromReceipt(trxReceipt)
		if err != nil {
			return fmt.Errorf("unable to extract EOS signed transaction from transaction receipt: %s", err)
		}

		signedTrx := deos.SignedTransactionToDEOS(signedTransaction)
		pubKeyProto := &pbdeos.PublicKeys{
			PublicKeys: deos.GetPublicKeysFromSignedTransaction(db.writerChainID, signedTransaction),
		}

		trxRow := &pbrows.TrxRow{
			Receipt:    trxReceipt,
			SignedTrx:  signedTrx,
			PublicKeys: pubKeyProto,
		}

		//zlog.Debug("put trx", zap.String("trx_id", trxReceipt.Id))
		key := Keys.PackTrxsKey(trxReceipt.Id, blk.Id)
		err = db.store.Put(ctx, key, db.enc.MustProto(trxRow))

		if err != nil {
			return fmt.Errorf("put trx: write to db: %w", err)
		}
	}

	return nil
}

func (db *DB) putTransactionTraces(ctx context.Context, blk *pbdeos.Block) error {
	for _, trxTrace := range blk.TransactionTraces {

		// CHECK: can we have multiple dtrxops for the same transactionId in the same block?
		for _, dtrxOp := range trxTrace.DtrxOps {
			extDtrxOp := dtrxOp.ToExtDTrxOp(blk, trxTrace)

			dtrxRow := &pbrows.DtrxRow{}
			if dtrxOp.IsCreateOperation() {
				dtrxRow.SignedTrx = dtrxOp.Transaction
				dtrxRow.CreatedBy = extDtrxOp
			} else if dtrxOp.IsCancelOperation() {
				dtrxRow.CanceledBy = extDtrxOp
			}

			// TODO: check make sure this is like bigtable implementation.
			key := Keys.PackDtrxsKey(dtrxOp.TransactionId, blk.Id)
			//zlog.Debug("put dtrxRow", zap.String("trx_id", trxTrace.Id), zap.ByteString("key", key))
			if err := db.store.Put(ctx, key, db.enc.MustProto(dtrxRow)); err != nil {
				return fmt.Errorf("put dtrxRow: write to db: %w", err)
			}
		}

		trxTraceRow := &pbrows.TrxTraceRow{
			BlockHeader: blk.Header,
			TrxTrace:    trxTrace,
		}
		//zlog.Debug("put trxTraceRow", zap.String("trx_id", trxTrace.Id))
		key := Keys.PackTrxTracesKey(trxTrace.Id, blk.Id)
		if err := db.store.Put(ctx, key, db.enc.MustProto(trxTraceRow)); err != nil {
			return fmt.Errorf("put trxTraceRow: write to db: %w", err)
		}
	}

	return nil
}

func (db *DB) putNewAccount(ctx context.Context, blk *pbdeos.Block, trace *pbdeos.TransactionTrace, act *pbdeos.ActionTrace) error {
	t, err := ptypes.TimestampProto(blk.MustTime())
	if err != nil {
		return fmt.Errorf("block time to proto: %w", err)
	}

	acctRow := &pbrows.AccountRow{
		Name:      act.GetData("name").String(),
		Creator:   act.GetData("creator").String(),
		BlockTime: t,
		BlockId:   blk.Id,
		TrxId:     trace.Id,
	}
	//zlog.Debug("put acctRow", zap.String("trx_id", trace.Id))
	key := Keys.PackAccountKey(acctRow.Name)
	if err := db.store.Put(ctx, key, db.enc.MustProto(acctRow)); err != nil {
		return fmt.Errorf("put acctRow: write to db: %w", err)
	}

	return nil
}

func (db *DB) putImplicitTransactions(ctx context.Context, blk *pbdeos.Block) error {

	for _, trxOp := range blk.ImplicitTransactionOps {
		implTrxRow := &pbrows.ImplicitTrxRow{
			Name:      trxOp.Name,
			SignedTrx: trxOp.Transaction,
		}

		//zlog.Debug("put implTrx", zap.String("trx_id", trxOp.TransactionId))

		key := Keys.PackImplicitTrxsKey(trxOp.TransactionId, blk.Id)
		if err := db.store.Put(ctx, key, db.enc.MustProto(implTrxRow)); err != nil {
			return fmt.Errorf("put implTrx: write to db: %w", err)
		}
	}

	return nil
}

func (db *DB) getRefs(blk *pbdeos.Block) (implicitTrxRefs, trxRefs, tracesRefs *pbdeos.TransactionRefs) {
	implicitTrxRefs = &pbdeos.TransactionRefs{}
	for _, trxOp := range blk.ImplicitTransactionOps {
		implicitTrxRefs.Hashes = append(implicitTrxRefs.Hashes, eosdb.MustHexDecode(trxOp.TransactionId))
	}

	trxRefs = &pbdeos.TransactionRefs{}
	for _, trx := range blk.Transactions {
		trxRefs.Hashes = append(trxRefs.Hashes, eosdb.MustHexDecode(trx.Id))
	}

	tracesRefs = &pbdeos.TransactionRefs{}
	for _, trx := range blk.TransactionTraces {
		tracesRefs.Hashes = append(tracesRefs.Hashes, eosdb.MustHexDecode(trx.Id))
	}

	return
}

func (db *DB) putBlock(ctx context.Context, blk *pbdeos.Block) error {
	implicitTrxRefs, trxRefs, tracesRefs := db.getRefs(blk)

	holdTransactions := blk.Transactions
	holdTransactionTraces := blk.TransactionTraces
	holdImplicitTransactionOps := blk.ImplicitTransactionOps

	blk.ImplicitTransactionOps = nil
	blk.Transactions = nil
	blk.TransactionTraces = nil

	blockRow := &pbrows.BlockRow{
		Block:           blk,
		ImplicitTrxRefs: implicitTrxRefs,
		TrxRefs:         trxRefs,
		TraceRefs:       tracesRefs,
	}

	zlog.Debug("put block", zap.String("block_id", blk.Id))
	key := Keys.PackBlocksKey(blk.Id)
	if err := db.store.Put(ctx, key, db.enc.MustProto(blockRow)); err != nil {
		return fmt.Errorf("put block: write to db: %w", err)
	}

	blk.ImplicitTransactionOps = holdImplicitTransactionOps
	blk.Transactions = holdTransactions
	blk.TransactionTraces = holdTransactionTraces

	return nil
}

var oneByte = []byte{0x01}

func (db *DB) UpdateNowIrreversibleBlock(ctx context.Context, blk *pbdeos.Block) error {
	blockTime := blk.MustTime()

	if err := db.store.Put(ctx, Keys.PackTimelineKey(true, blockTime, blk.Id), oneByte); err != nil {
		return err
	}
	if err := db.store.Put(ctx, Keys.PackTimelineKey(false, blockTime, blk.Id), oneByte); err != nil {
		return err
	}

	// Specialized indexing for `newaccount` on the chain.
	for _, trxTrace := range blk.TransactionTraces {
		for _, act := range trxTrace.ActionTraces {
			if act.Account() == "eosio" && act.Receiver == "eosio" && act.Name() == "newaccount" {
				if err := db.putNewAccount(ctx, blk, trxTrace, act); err != nil {
					return fmt.Errorf("failed to put new account: %w", err)
				}
			}
		}
	}

	zlog.Debug("adding irreversible block", zap.String("block_id", blk.Id))
	if err := db.store.Put(ctx, Keys.PackIrrBlocksKey(blk.Id), oneByte); err != nil {
		return err
	}

	return nil
}
