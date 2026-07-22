package main

// flow.go — §9 post-type checks: control-flow analysis over the typed AST.
//
//   - all-paths-return: a function with results must return or diverge
//     (panic, infinite loop) on every path
//   - unreachable code: statements after a diverging statement are warned
//
// Divergence is computed from the checker's side tables: a statement
// diverges if it returns, calls panic (tNever), is an exhaustive match
// whose arms all diverge, or is an infinite loop with no break.

// checkFlow runs the flow checks for every function in the file. It is
// called from check() after all bodies have been type-checked, so the
// types side table is fully populated.
func (c *checker) checkFlow(f *File) {
	for _, d := range f.Decls {
		switch fn := d.(type) {
		case *FuncDecl:
			if fn.Body == nil { // native
				continue
			}
			c.scanUnreachable(fn.Body.List)
			if ft := c.funcs[fn.Name]; ft != nil && len(ft.results) > 0 {
				if !c.listDiverges(fn.Body.List) {
					c.diag.errorf(fn.Line, "function %s: missing return (some path falls through without returning)", fn.Name)
				}
			}
		case *ImplDecl:
			tn := implTypeName(fn.Type)
			for _, m := range fn.Methods {
				if m.Body == nil || tn == "" || c.methods[tn] == nil {
					continue
				}
				c.scanUnreachable(m.Body.List)
				if ft := c.methods[tn][m.Name]; ft != nil && len(ft.results) > 0 {
					if !c.listDiverges(m.Body.List) {
						c.diag.errorf(m.Line, "method %s: missing return (some path falls through without returning)", m.Name)
					}
				}
			}
		case *BehaviorDecl:
			for _, m := range fn.Methods {
				if m.Body == nil {
					continue
				}
				c.scanUnreachable(m.Body.List)
				if ft := c.behaviorSigs[fn.Name][m.Name]; ft != nil && len(ft.results) > 0 {
					if !c.listDiverges(m.Body.List) {
						c.diag.errorf(m.Line, "default method %s: missing return (some path falls through without returning)", m.Name)
					}
				}
			}
		}
	}
}

// ---------- divergence ----------

func (c *checker) listDiverges(list []Stmt) bool {
	for _, s := range list {
		if c.stmtDiverges(s) {
			return true
		}
	}
	return false
}

func (c *checker) stmtDiverges(s Stmt) bool {
	switch st := s.(type) {
	case *ReturnStmt, *ContinueStmt:
		return true
	case *Block:
		return c.listDiverges(st.List)
	case *ExprStmt:
		return c.exprDiverges(st.X)
	case *IfStmt:
		if st.Else == nil {
			return false
		}
		return c.listDiverges(st.Then.List) && c.stmtDiverges(st.Else)
	case *LoopStmt:
		// `loop { ... }` diverges if no break targets it
		return !bodyCanBreak(st.Body.List)
	case *ForStmt:
		// `for { ... }` without a condition diverges if no break targets it
		return st.Cond == nil && !bodyCanBreak(st.Body.List)
	}
	return false
}

func (c *checker) exprDiverges(e Expr) bool {
	if isNever(c.types[e]) {
		return true
	}
	if m, ok := e.(*MatchExpr); ok {
		return c.matchDiverges(m)
	}
	return false
}

// matchDiverges: a match diverges if it is exhaustive (cannot fall
// through) and every arm diverges.
func (c *checker) matchDiverges(m *MatchExpr) bool {
	if !c.matchExhaustive(m) {
		return false
	}
	for i := range m.Arms {
		a := &m.Arms[i]
		if a.BodyExpr != nil {
			if !c.exprDiverges(a.BodyExpr) {
				return false
			}
		} else if !c.listDiverges(a.Body) {
			return false
		}
	}
	return len(m.Arms) > 0
}

// matchExhaustive mirrors the coverage rules from checkMatchSubject:
// unguarded wildcard or catch-all binding, or every enum variant covered
// by an unguarded arm. Channel matches are blocking selects — conservatively
// not exhaustive for divergence purposes.
func (c *checker) matchExhaustive(m *MatchExpr) bool {
	st := c.types[m.Subject]
	en, isEnum := st.(*tEnum)
	for i := range m.Arms {
		a := &m.Arms[i]
		if a.Guard != nil {
			continue
		}
		switch p := a.Pat.(type) {
		case *WildcardPat:
			return true
		case *IdentPat:
			if isEnum && findVariant(en.decl, p.Name) != nil {
				continue // unit-variant pattern, counted below
			}
			return true // catch-all binding
		}
	}
	if !isEnum {
		return false
	}
	covered := map[string]bool{}
	for i := range m.Arms {
		a := &m.Arms[i]
		if a.Guard != nil {
			continue
		}
		switch p := a.Pat.(type) {
		case *VariantPat:
			covered[p.Name] = true
		case *IdentPat:
			covered[p.Name] = true
		}
	}
	for _, v := range en.decl.Variants {
		if !covered[v.Name] {
			return false
		}
	}
	return true
}

// bodyHasBreakLoop reports whether a `break loop` (labeled) appears in the
// body, so the emitter only emits a loop label when it is actually used.
// A `break loop` inside a nested go++ loop targets the inner loop, so the
// scan does not descend into those; a labeled break inside a plain for or
// a match arm still targets this loop's label.
func bodyHasBreakLoop(list []Stmt) bool {
	for _, s := range list {
		if stmtHasBreakLoop(s) {
			return true
		}
	}
	return false
}

func stmtHasBreakLoop(s Stmt) bool {
	switch st := s.(type) {
	case *BreakStmt:
		return st.Label == "loop"
	case *Block:
		return bodyHasBreakLoop(st.List)
	case *IfStmt:
		return bodyHasBreakLoop(st.Then.List) || (st.Else != nil && stmtHasBreakLoop(st.Else))
	case *ForStmt:
		return bodyHasBreakLoop(st.Body.List)
	case *LoopStmt:
		return false // a nested loop has its own label
	case *ExprStmt:
		if m, ok := st.X.(*MatchExpr); ok {
			for i := range m.Arms {
				if bodyHasBreakLoop(m.Arms[i].Body) {
					return true
				}
			}
		}
	}
	return false
}

// bodyCanBreak reports whether a loop body contains a break that targets
// the loop itself (breaks inside nested loops or channel-select matches
// target those instead).
func bodyCanBreak(list []Stmt) bool {
	for _, s := range list {
		if stmtCanBreak(s) {
			return true
		}
	}
	return false
}

func stmtCanBreak(s Stmt) bool {
	switch st := s.(type) {
	case *BreakStmt:
		return true
	case *Block:
		return bodyCanBreak(st.List)
	case *IfStmt:
		return bodyCanBreak(st.Then.List) || (st.Else != nil && stmtCanBreak(st.Else))
	case *LoopStmt, *ForStmt:
		return false // nested loop captures breaks
	case *ExprStmt:
		if m, ok := st.X.(*MatchExpr); ok {
			return matchCanBreak(m)
		}
	}
	return false
}

func matchCanBreak(m *MatchExpr) bool {
	for i := range m.Arms {
		switch m.Arms[i].Pat.(type) {
		case *RecvPat, *SendPat, *AfterPat, *ClosedPat:
			return false // select arms capture breaks
		}
	}
	for i := range m.Arms {
		if bodyCanBreak(m.Arms[i].Body) {
			return true
		}
	}
	return false
}

// ---------- unreachable code ----------

// scanUnreachable warns about statements after a diverging statement
// (one warning per statement list), and recurses into all nested lists.
func (c *checker) scanUnreachable(list []Stmt) {
	diverged := false
	for _, s := range list {
		if diverged {
			c.diag.warnf(stmtLine(s), "unreachable code")
			diverged = false // one warning per list is enough
		}
		c.scanStmtChildren(s)
		if c.stmtDiverges(s) {
			diverged = true
		}
	}
}

func (c *checker) scanStmtChildren(s Stmt) {
	switch st := s.(type) {
	case *Block:
		c.scanUnreachable(st.List)
	case *IfStmt:
		c.scanExpr(st.Cond)
		c.scanUnreachable(st.Then.List)
		if st.Else != nil {
			c.scanStmtChildren(st.Else)
		}
	case *ForStmt:
		if st.Init != nil {
			c.scanStmtChildren(st.Init)
		}
		if st.Cond != nil {
			c.scanExpr(st.Cond)
		}
		c.scanUnreachable(st.Body.List)
	case *LoopStmt:
		c.scanUnreachable(st.Body.List)
	case *VarStmt:
		if st.Init != nil {
			c.scanExpr(st.Init)
		}
	case *ExprStmt:
		c.scanExpr(st.X)
	case *AssignStmt:
		for _, e := range st.Rhs {
			c.scanExpr(e)
		}
	case *ReturnStmt:
		for _, e := range st.Results {
			c.scanExpr(e)
		}
	}
}

// scanExpr finds match expressions nested inside expressions; their arm
// bodies are statement lists of their own.
func (c *checker) scanExpr(e Expr) {
	switch ex := e.(type) {
	case *MatchExpr:
		if ex.Subject != nil {
			c.scanExpr(ex.Subject)
		}
		for i := range ex.Arms {
			a := &ex.Arms[i]
			if a.Guard != nil {
				c.scanExpr(a.Guard)
			}
			if a.BodyExpr != nil {
				c.scanExpr(a.BodyExpr)
			} else {
				c.scanUnreachable(a.Body)
			}
			if p, ok := a.Pat.(*BoolPat); ok {
				c.scanExpr(p.X)
			}
		}
	case *BinaryExpr:
		c.scanExpr(ex.X)
		c.scanExpr(ex.Y)
	case *UnaryExpr:
		c.scanExpr(ex.X)
	case *CallExpr:
		c.scanExpr(ex.Fun)
		for _, a := range ex.Args {
			c.scanExpr(a)
		}
	case *SelectorExpr:
		c.scanExpr(ex.X)
	case *IndexExpr:
		c.scanExpr(ex.X)
		for _, ix := range ex.Index {
			c.scanExpr(ix)
		}
	case *StructLitExpr:
		for _, fv := range ex.Fields {
			c.scanExpr(fv.Value)
		}
	case *TryExpr:
		c.scanExpr(ex.X)
	}
}

func stmtLine(s Stmt) int {
	switch st := s.(type) {
	case *Block:
		return st.Line
	case *VarStmt:
		return st.Line
	case *ExprStmt:
		return st.Line
	case *AssignStmt:
		return st.Line
	case *IfStmt:
		return st.Line
	case *ForStmt:
		return st.Line
	case *ForInStmt:
		return st.Line
	case *LoopStmt:
		return st.Line
	case *BreakStmt:
		return st.Line
	case *ContinueStmt:
		return st.Line
	case *DeferStmt:
		return st.Line
	case *ReturnStmt:
		return st.Line
	case *IncDecStmt:
		return st.Line
	}
	return 0
}
