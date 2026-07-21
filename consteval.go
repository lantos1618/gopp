package main

import (
	"math/big"
	"strconv"
)

// consteval.go — §10 const evaluation for `comptime expr`.
//
// A tiny interpreter over the checked AST with the same arithmetic rules
// as the runtime (§29): integer math is exact (big.Int, matching Go's
// arbitrary-precision constants), division/modulo by zero is a compile
// error, overflow of a TYPED integer is a compile error, and everything
// runs under fuel so a pathological expression can never hang the
// compiler. Function calls (other than conversions), variables, and any
// other runtime construct are "not a constant expression" — full
// comptime functions stay deferred.

const constFuelLimit = 100000

type constKind int

const (
	ckInt constKind = iota
	ckFloat
	ckString
	ckBool
	ckRune
	ckDuration // nanosecond count, like time.Duration
	ckList     // comptime list (§10 metaprogramming)
	ckRecord   // comptime record — a live handle onto an AST node
	ckVoid     // no value (comptime call to a void function)
)

type constVal struct {
	kind constKind
	i    *big.Int    // ckInt, ckRune, ckDuration
	f    float64     // ckFloat
	s    string      // ckString
	b    bool        // ckBool
	l    []constVal  // ckList
	add  func(constVal) error // ckList: optional .add(...) method (live lists)
	r    *metaRecord // ckRecord
}

func intVal(v *big.Int) constVal  { return constVal{kind: ckInt, i: v} }
func boolVal(b bool) constVal     { return constVal{kind: ckBool, b: b} }
func floatVal(f float64) constVal { return constVal{kind: ckFloat, f: f} }

func isConstNum(v constVal) bool {
	switch v.kind {
	case ckInt, ckFloat, ckRune, ckDuration:
		return true
	}
	return false
}

func constToFloat(v constVal) float64 {
	if v.kind == ckFloat {
		return v.f
	}
	f, _ := new(big.Float).SetInt(v.i).Float64()
	return f
}

func lineOf(e Expr) int {
	switch ex := e.(type) {
	case *BasicLit:
		return ex.Line
	case *Ident:
		return ex.Line
	case *BinaryExpr:
		return ex.Line
	case *UnaryExpr:
		return ex.Line
	case *CallExpr:
		return ex.Line
	case *ComptimeExpr:
		return ex.Line
	case *SelectorExpr:
		return ex.Line
	case *IndexExpr:
		return ex.Line
	case *MatchExpr:
		return ex.Line
	case *StructLitExpr:
		return ex.Line
	case *MakeChanExpr:
		return ex.Line
	case *TryExpr:
		return ex.Line
	}
	return 0
}

// colOf is lineOf for columns (§11): the caret position of a diagnostic
// attached to an arbitrary expression node.
func colOf(e Expr) int {
	switch ex := e.(type) {
	case *BasicLit:
		return ex.Col
	case *Ident:
		return ex.Col
	case *BinaryExpr:
		return ex.Col
	case *UnaryExpr:
		return ex.Col
	case *CallExpr:
		return ex.Col
	case *ComptimeExpr:
		return ex.Col
	case *SelectorExpr:
		return ex.Col
	case *IndexExpr:
		return ex.Col
	case *MatchExpr:
		return ex.Col
	case *StructLitExpr:
		return ex.Col
	case *MakeChanExpr:
		return ex.Col
	case *TryExpr:
		return ex.Col
	}
	return 0
}

// checkComptime types the inner expression (normal rules, so c.types is
// fully populated), then evaluates it and records the constant in the
// constVals side table for the emitter. The result type is the inner
// type — untyped literals stay untyped, so §7 adoption still applies —
// except that an untyped integer must fit its default type (int) right
// here, since there is no later context to catch it.
func (c *checker) checkComptime(ex *ComptimeExpr) Type {
	inner := c.checkExpr(ex.X)
	if isErr(inner) {
		return terr
	}
	fuel := constFuelLimit
	v, ok := c.constEval(ex.X, &fuel)
	if !ok {
		return terr
	}
	c.constVals[ex] = v
	if _, untyped := inner.(tUntypedInt); untyped && v.kind == ckInt && !fitsBigInt(v.i, "int") {
		c.diag.errorfAt(ex.Line, ex.Col, "constant %s overflows int", v.i.String())
		return terr
	}
	return inner
}

// constEval evaluates e, which has already been type-checked. On failure
// it records exactly one diagnostic and returns ok=false; callers
// propagate without diagnosing again (§11).
func (c *checker) constEval(e Expr, fuel *int) (constVal, bool) {
	*fuel--
	if *fuel <= 0 {
		c.diag.errorfAt(lineOf(e), colOf(e), "comptime evaluation limit reached")
		return constVal{}, false
	}
	switch ex := e.(type) {
	case *BasicLit:
		switch ex.Kind {
		case kInt:
			v, ok := new(big.Int).SetString(ex.Value, 0)
			if !ok {
				return c.constFail(ex.Line, ex.Col, "invalid integer constant %s", ex.Value)
			}
			return intVal(v), true
		case kFloat:
			f, err := strconv.ParseFloat(ex.Value, 64)
			if err != nil {
				return c.constFail(ex.Line, ex.Col, "invalid float constant %s", ex.Value)
			}
			return floatVal(f), true
		case kString:
			s, err := strconv.Unquote(ex.Value)
			if err != nil {
				return c.constFail(ex.Line, ex.Col, "invalid string constant %s", ex.Value)
			}
			return constVal{kind: ckString, s: s}, true
		case kRune:
			s, err := strconv.Unquote(ex.Value)
			if err != nil {
				return c.constFail(ex.Line, ex.Col, "invalid rune constant %s", ex.Value)
			}
			r := []rune(s)
			if len(r) != 1 {
				return c.constFail(ex.Line, ex.Col, "invalid rune constant %s", ex.Value)
			}
			return constVal{kind: ckRune, i: big.NewInt(int64(r[0]))}, true
		}
	case *Ident:
		// a local binding of the name is a variable, never a constant —
		// only the prelude globals (and true/false keywords) qualify
		if _, isVar := c.cur.lookup(ex.Name); !isVar {
			switch ex.Name {
			case "true":
				return boolVal(true), true
			case "false":
				return boolVal(false), true
			}
		}
		shadowed := false
		for s := c.cur; s != nil && s != c.globals; s = s.parent {
			if _, ok := s.vars[ex.Name]; ok {
				shadowed = true
			}
		}
		if !shadowed {
			switch ex.Name {
			case "ms":
				return constVal{kind: ckDuration, i: big.NewInt(1_000_000)}, true
			case "second":
				return constVal{kind: ckDuration, i: big.NewInt(1_000_000_000)}, true
			case "minute":
				return constVal{kind: ckDuration, i: big.NewInt(60_000_000_000)}, true
			}
		}
		return c.constFail(ex.Line, ex.Col, "%s is not a constant", ex.Name)
	case *ComptimeExpr:
		return c.constEval(ex.X, fuel)
	case *UnaryExpr:
		x, ok := c.constEval(ex.X, fuel)
		if !ok {
			return constVal{}, false
		}
		return c.constUnary(ex, x)
	case *BinaryExpr:
		x, ok := c.constEval(ex.X, fuel)
		if !ok {
			return constVal{}, false
		}
		y, ok := c.constEval(ex.Y, fuel)
		if !ok {
			return constVal{}, false
		}
		return c.constBinary(ex, x, y)
	case *CallExpr:
		return c.constCall(ex, fuel)
	}
	return c.constFail(lineOf(e), colOf(e), "not a constant expression")
}

func (c *checker) constFail(line, col int, format string, args ...interface{}) (constVal, bool) {
	c.diag.errorfAt(line, col, format, args...)
	return constVal{}, false
}

func (c *checker) constUnary(ex *UnaryExpr, x constVal) (constVal, bool) {
	switch ex.Op {
	case "!":
		if x.kind == ckBool {
			return boolVal(!x.b), true
		}
	case "+":
		if isConstNum(x) {
			return x, true
		}
	case "-":
		switch x.kind {
		case ckInt, ckRune, ckDuration:
			v := intVal(new(big.Int).Neg(x.i))
			v.kind = x.kind
			return c.constRange(ex, v)
		case ckFloat:
			return floatVal(-x.f), true
		}
	case "^":
		if x.kind == ckInt {
			return c.constRange(ex, intVal(new(big.Int).Not(x.i)))
		}
	}
	return c.constFail(ex.Line, ex.Col, "not a constant expression: %s", ex.Op)
}

// constRange applies the §29 overflow rule: when the expression node's
// sema type is a sized integer, the computed value must fit it.
func (c *checker) constRange(ex Expr, v constVal) (constVal, bool) {
	if v.kind != ckInt && v.kind != ckRune && v.kind != ckDuration {
		return v, true
	}
	if b, ok := c.types[ex].(tBasic); ok && isSizedInt(b.name) && !fitsBigInt(v.i, b.name) {
		return c.constFail(lineOf(ex), colOf(ex), "constant %s overflows %s", v.i.String(), b.name)
	}
	return v, true
}

func isSizedInt(name string) bool {
	switch name {
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"byte", "rune", "duration":
		return true
	}
	return false
}

func (c *checker) constBinary(ex *BinaryExpr, x, y constVal) (constVal, bool) {
	if x.kind == ckBool && y.kind == ckBool {
		switch ex.Op {
		case "&&":
			return boolVal(x.b && y.b), true
		case "||":
			return boolVal(x.b || y.b), true
		case "==":
			return boolVal(x.b == y.b), true
		case "!=":
			return boolVal(x.b != y.b), true
		}
		return c.constFail(ex.Line, ex.Col, "invalid operation: bool %s bool in constant expression", ex.Op)
	}
	if x.kind == ckString && y.kind == ckString {
		switch ex.Op {
		case "+":
			return constVal{kind: ckString, s: x.s + y.s}, true
		case "==":
			return boolVal(x.s == y.s), true
		case "!=":
			return boolVal(x.s != y.s), true
		case "<":
			return boolVal(x.s < y.s), true
		case "<=":
			return boolVal(x.s <= y.s), true
		case ">":
			return boolVal(x.s > y.s), true
		case ">=":
			return boolVal(x.s >= y.s), true
		}
		return c.constFail(ex.Line, ex.Col, "invalid operation: string %s string in constant expression", ex.Op)
	}
	if isConstNum(x) && isConstNum(y) {
		if x.kind == ckFloat || y.kind == ckFloat {
			return c.constBinaryFloat(ex, constToFloat(x), constToFloat(y))
		}
		return c.constBinaryInt(ex, x, y)
	}
	return c.constFail(ex.Line, ex.Col, "not a constant expression")
}

func (c *checker) constBinaryFloat(ex *BinaryExpr, x, y float64) (constVal, bool) {
	switch ex.Op {
	case "+":
		return floatVal(x + y), true
	case "-":
		return floatVal(x - y), true
	case "*":
		return floatVal(x * y), true
	case "/":
		if y == 0 {
			return c.constFail(ex.Line, ex.Col, "division by zero in constant expression")
		}
		return floatVal(x / y), true
	case "==":
		return boolVal(x == y), true
	case "!=":
		return boolVal(x != y), true
	case "<":
		return boolVal(x < y), true
	case "<=":
		return boolVal(x <= y), true
	case ">":
		return boolVal(x > y), true
	case ">=":
		return boolVal(x >= y), true
	}
	return c.constFail(ex.Line, ex.Col, "invalid operation: float %s float in constant expression", ex.Op)
}

func (c *checker) constBinaryInt(ex *BinaryExpr, x, y constVal) (constVal, bool) {
	// comparisons first: they produce bool and never overflow
	cmp := 0
	switch ex.Op {
	case "==", "!=", "<", "<=", ">", ">=":
		cmp = x.i.Cmp(y.i)
		switch ex.Op {
		case "==":
			return boolVal(cmp == 0), true
		case "!=":
			return boolVal(cmp != 0), true
		case "<":
			return boolVal(cmp < 0), true
		case "<=":
			return boolVal(cmp <= 0), true
		case ">":
			return boolVal(cmp > 0), true
		case ">=":
			return boolVal(cmp >= 0), true
		}
	}
	var res *big.Int
	switch ex.Op {
	case "+":
		res = new(big.Int).Add(x.i, y.i)
	case "-":
		res = new(big.Int).Sub(x.i, y.i)
	case "*":
		res = new(big.Int).Mul(x.i, y.i)
	case "/":
		if y.i.Sign() == 0 {
			return c.constFail(ex.Line, ex.Col, "division by zero in constant expression")
		}
		res = new(big.Int).Quo(x.i, y.i) // truncated, like Go integers
	case "%":
		if y.i.Sign() == 0 {
			return c.constFail(ex.Line, ex.Col, "division by zero in constant expression")
		}
		res = new(big.Int).Rem(x.i, y.i)
	case "&":
		res = new(big.Int).And(x.i, y.i)
	case "|":
		res = new(big.Int).Or(x.i, y.i)
	case "^":
		res = new(big.Int).Xor(x.i, y.i)
	case "&^":
		res = new(big.Int).AndNot(x.i, y.i)
	case "<<", ">>":
		if y.i.Sign() < 0 || !y.i.IsInt64() || y.i.Int64() > 4096 {
			return c.constFail(ex.Line, ex.Col, "shift count %s is out of range", y.i.String())
		}
		n := uint(y.i.Int64())
		if ex.Op == "<<" {
			res = new(big.Int).Lsh(x.i, n)
		} else {
			res = new(big.Int).Rsh(x.i, n) // arithmetic, like Go
		}
	default:
		return c.constFail(ex.Line, ex.Col, "invalid operation in constant expression: %s", ex.Op)
	}
	// a duration on either side makes the result a duration (same rule
	// as arithType in sema); rune keeps rune-ness for emission
	v := intVal(res)
	if x.kind == ckDuration || y.kind == ckDuration {
		v.kind = ckDuration
	} else if x.kind == ckRune || y.kind == ckRune {
		v.kind = ckRune
	}
	return c.constRange(ex, v)
}

// constCall evaluates the only calls allowed at compile time: explicit
// conversions. Sema already validated legality (checkConversion ran as
// part of checkExpr), so this only computes and range-checks.
func (c *checker) constCall(ex *CallExpr, fuel *int) (constVal, bool) {
	id, ok := ex.Fun.(*Ident)
	if !ok || !basicTypes[id.Name] || len(ex.Args) != 1 {
		return c.constFail(ex.Line, ex.Col, "not a constant expression (only conversions may be called)")
	}
	v, ok := c.constEval(ex.Args[0], fuel)
	if !ok {
		return constVal{}, false
	}
	to := id.Name
	switch {
	case isSizedInt(to):
		switch v.kind {
		case ckInt, ckRune, ckDuration:
			if !fitsBigInt(v.i, to) {
				return c.constFail(ex.Line, ex.Col, "constant %s overflows %s", v.i.String(), to)
			}
			if to == "rune" {
				return constVal{kind: ckRune, i: v.i}, true
			}
			return intVal(v.i), true
		case ckFloat:
			iv, _ := big.NewFloat(v.f).Int(nil) // truncates toward zero, like Go
			if !fitsBigInt(iv, to) {
				return c.constFail(ex.Line, ex.Col, "constant %v overflows %s", v.f, to)
			}
			return intVal(iv), true
		}
	case to == "float32" || to == "float64":
		if isConstNum(v) {
			return floatVal(constToFloat(v)), true
		}
	case to == "string":
		if v.kind == ckRune {
			return constVal{kind: ckString, s: string(rune(v.i.Int64()))}, true
		}
	case to == "rune":
		if v.kind == ckString && len([]rune(v.s)) == 1 {
			return constVal{kind: ckRune, i: big.NewInt(int64([]rune(v.s)[0]))}, true
		}
	}
	return c.constFail(ex.Line, ex.Col, "not a constant expression")
}

// fitsBigInt reports whether v fits the named integer type.
func fitsBigInt(v *big.Int, name string) bool {
	var bits uint
	signed := true
	switch name {
	case "int8":
		bits = 8
	case "int16":
		bits = 16
	case "int32", "rune":
		bits = 32
	case "int64", "int", "duration":
		bits = 64
	case "uint8", "byte":
		bits, signed = 8, false
	case "uint16":
		bits, signed = 16, false
	case "uint32":
		bits, signed = 32, false
	case "uint64", "uint", "uintptr":
		bits, signed = 64, false
	default:
		return true
	}
	one := big.NewInt(1)
	if signed {
		hi := new(big.Int).Lsh(one, bits-1) // 2^(bits-1)
		return v.Cmp(new(big.Int).Neg(hi)) >= 0 && v.Cmp(new(big.Int).Sub(hi, one)) <= 0
	}
	max := new(big.Int).Sub(new(big.Int).Lsh(one, bits), one)
	return v.Sign() >= 0 && v.Cmp(max) <= 0
}
