package badger

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net/url"

	"github.com/dgraph-io/badger/v2"
	"github.com/dgraph-io/badger/v2/options"
	"github.com/dfuse-io/kvdb/store"
	"go.uber.org/zap"
)

type Store struct {
	db         *badger.DB
	writeBatch *badger.WriteBatch
	compressor store.Compressor
}

func init() {
	store.Register(&store.Registration{
		Name:        "badger",
		Title:       "Badger",
		FactoryFunc: NewStore,
	})
}

func NewStore(dsnString string) (store.KVStore, error) {
	dsn, err := url.Parse(dsnString)
	if err != nil {
		return nil, fmt.Errorf("badger new: dsn: %w", err)
	}

	zlog.Debug("setting up badger db",
		zap.String("dsn.path", dsnString),
	)

	db, err := badger.Open(badger.DefaultOptions(dsn.Path).WithLogger(nil).WithCompression(options.None))
	if err != nil {
		return nil, fmt.Errorf("badger new: open badger db: %w", err)
	}

	compressor, err := store.NewCompressor(dsn.Query().Get("compression"))
	if err != nil {
		return nil, err
	}

	s := &Store{
		db:         db,
		compressor: compressor,
	}

	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Put(ctx context.Context, key, value []byte) (err error) {
	zlog.Debug("putting", zap.String("key", hex.EncodeToString(key)))
	if s.writeBatch == nil {
		s.writeBatch = s.db.NewWriteBatch()
	}

	value = s.compressor.Compress(value)

	err = s.writeBatch.SetEntry(badger.NewEntry(key, value))
	if err == badger.ErrTxnTooBig {
		zlog.Debug("txn too big pre-emptively pushing")
		if err := s.writeBatch.Flush(); err != nil {
			return err
		}

		s.writeBatch = s.db.NewWriteBatch()
		err := s.writeBatch.SetEntry(badger.NewEntry(key, value))
		if err != nil {
			return fmt.Errorf("after txn too big: %w", err)
		}
	}

	return nil
}

func (s *Store) FlushPuts(ctx context.Context) error {
	if s.writeBatch == nil {
		return nil
	}
	err := s.writeBatch.Flush()
	if err != nil {
		return err
	}
	s.writeBatch = s.db.NewWriteBatch()
	return nil
}

func (s *Store) Get(ctx context.Context, key []byte) (value []byte, err error) {
	err = s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return store.ErrNotFound
			}
			return err
		}

		// TODO: optimize: if we're going to decompress, we can use the `item.Value` instead
		// of making a copy
		value, err = item.ValueCopy(nil)
		if err != nil {
			return err
		}

		value, err = s.compressor.Decompress(value)
		if err != nil {
			return err
		}

		return nil
	})
	return
}

func (s *Store) BatchGet(ctx context.Context, keys [][]byte) *store.Iterator {
	kr := store.NewIterator(ctx)

	go func() {
		err := s.db.View(func(txn *badger.Txn) error {
			for _, key := range keys {
				item, err := txn.Get(key)
				if err != nil {
					return err
				}

				value, err := item.ValueCopy(nil)
				if err != nil {
					return err
				}

				value, err = s.compressor.Decompress(value)
				if err != nil {
					return err
				}
				kr.PushItem(&store.KV{item.KeyCopy(nil), value})

				// TODO: make sure this is conform and takes inspiration from `Scan`.. deals
				// with the `store.Iterator` properly
			}
			return nil
		})
		if err != nil {
			kr.PushError(err)
			return
		}
		kr.PushFinished()
	}()
	return kr
}

func (s *Store) Scan(ctx context.Context, start, exclusiveEnd []byte, limit int) *store.Iterator {
	sit := store.NewIterator(ctx)
	zlog.Debug("scanning", zap.String("start", hex.EncodeToString(start)), zap.String("exclusive_end", hex.EncodeToString(exclusiveEnd)), zap.Int("limit", limit))
	go func() {
		err := s.db.View(func(txn *badger.Txn) error {
			bit := txn.NewIterator(badger.DefaultIteratorOptions)
			defer bit.Close()

			count := 0
			for bit.Seek(start); bit.Valid() && bytes.Compare(bit.Item().Key(), exclusiveEnd) == -1; bit.Next() {
				count++
				if err := sit.Context().Err(); err != nil {
					return err
				}

				value, err := bit.Item().ValueCopy(nil)
				if err != nil {
					return err
				}

				value, err = s.compressor.Decompress(value)
				if err != nil {
					return err
				}

				sit.PushItem(&store.KV{bit.Item().KeyCopy(nil), value})

				if count == limit && limit > 0 {
					break
				}
			}
			return nil
		})
		if err != nil {
			sit.PushError(err)
			return
		}

		sit.PushFinished()
	}()

	return sit
}

func (s *Store) Prefix(ctx context.Context, prefix []byte) *store.Iterator {
	kr := store.NewIterator(ctx)
	zlog.Debug("prefix scanning ", zap.String("prefix", hex.EncodeToString(prefix)))
	go func() {
		err := s.db.View(func(txn *badger.Txn) error {
			options := badger.DefaultIteratorOptions
			it := txn.NewIterator(options)
			defer it.Close()

			count := 0
			for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
				count++
				if err := kr.Context().Err(); err != nil {
					return err
				}

				key := it.Item().KeyCopy(nil)
				value, err := it.Item().ValueCopy(nil)
				if err != nil {
					return err
				}

				value, err = s.compressor.Decompress(value)
				if err != nil {
					return err
				}

				kr.PushItem(&store.KV{key, value})
			}
			return nil
		})
		if err != nil {
			kr.PushError(err)
			return
		}

		kr.PushFinished()
	}()

	return kr
}