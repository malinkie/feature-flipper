/*
 * (c) 2017 Adobe.  All rights reserved.
 * This file is licensed to you under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License. You may obtain a copy
 * of the License at http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software distributed under
 * the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR REPRESENTATIONS
 * OF ANY KIND, either express or implied. See the License for the specific language
 * governing permissions and limitations under the License.
 */
package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/pborman/uuid"
	"gopkg.in/inconshreveable/log15.v2"
)

const (
	ctxKeyLog = iota
	ctxKeyParams
)

// allow for 3 retries of an outbound call plus a 1 second buffer
// see util/retry.go
const ContextTimeout = 15 * time.Second

var (
	bgContext = context.Background()
)

// CtxResponseWriter is a context-aware http.ResponseWriter.
// It prevents calls to the real http.ResponseWriter that may already be closed
// due to a context timeout
type CtxResponseWriter struct {
	w   http.ResponseWriter
	ctx context.Context
}

func (recv CtxResponseWriter) Header() http.Header {
	return recv.w.Header()
}

func (recv CtxResponseWriter) Write(bs []byte) (int, error) {
	err := recv.ctx.Err()
	if err != nil {
		return 0, err
	}

	return recv.w.Write(bs)
}

func (recv CtxResponseWriter) WriteHeader(i int) {
	if recv.ctx.Err() == nil {
		recv.w.WriteHeader(i)
	}
}

func Context(route string, inputLog log15.Logger, hdl Handle) httprouter.Handle {

	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {

		t0 := time.Now()

		var reqId uuid.UUID

		if xReqId := w.Header().Get("X-Request-Id"); xReqId != "" {

			reqId = uuid.Parse(xReqId)
		}

		if reqId == nil {

			reqId = uuid.NewRandom()
			w.Header().Set("X-Request-Id", reqId.String())
		}

		log := inputLog.New(
			"RequestId", reqId,
			"Path", r.URL.Path,
			"Method", r.Method)

		ctx, cancel := context.WithTimeout(bgContext, ContextTimeout)
		defer cancel()

		ctx = WithRequestLog(ctx, log)
		ctx = WithParams(ctx, ps)

		// doneChan is buffered because it's possible for ctx.Done() and
		// `doneChan <- struct{}{}` to happen at the same time.
		//
		// If `case _ = <-ctx.Done():` is selected first and doneChan was
		// unbuffered then `doneChan <- struct{}{}` would block forever and
		// leak goroutines.
		//
		// From the language spec:
		// If one or more of the communications can proceed, a single one that
		// can proceed is chosen via a uniform pseudo-random selection.
		// Otherwise, if there is a default case, that case is chosen. If there
		// is no default case, the "select" statement blocks until at least one
		// of the communications can proceed.
		doneChan := make(chan struct{}, 1)

		go func() {

			defer func() {
				if err := recover(); err != nil {

					log.Error("panic", "Error", err)
					w.WriteHeader(500)
					doneChan <- struct{}{}
				}
			}()

			wProxy := CtxResponseWriter{
				w:   w,
				ctx: ctx,
			}

			hdl(ctx, wProxy, r)

			doneChan <- struct{}{}
		}()

		select {
		case _ = <-ctx.Done():
			// use the real http.ResponseWriter
			w.WriteHeader(408)
		case _ = <-doneChan:
			// nothing to do
		}

		t1 := time.Now()
		log.Debug("Profile", "Duration", t1.Sub(t0).Nanoseconds()/1000000)
	}
}

func WithRequestLog(ctx context.Context, value log15.Logger) context.Context {

	return context.WithValue(ctx, ctxKeyLog, value)
}

func GetRequestLog(ctx context.Context) log15.Logger {

	value, ok := ctx.Value(ctxKeyLog).(log15.Logger)
	if ok {
		return value
	} else {
		packageLogger.Error("context error", "type", "ctxKeyLog")

		// attempt to track this even though the request-scoped logger is missing
		reqId := uuid.NewRandom()
		return packageLogger.New("fallback", "fallback", "RequestId", reqId)
	}
}

func WithParams(ctx context.Context, value httprouter.Params) context.Context {

	return context.WithValue(ctx, ctxKeyParams, value)
}

func GetParams(ctx context.Context) httprouter.Params {

	value, ok := ctx.Value(ctxKeyParams).(httprouter.Params)
	if ok {
		return value
	} else {
		packageLogger.Error("context error", "type", "ctxKeyParams")
		return make(httprouter.Params, 0)
	}
}