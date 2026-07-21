package main

// ast.go — go++ v2 compiler: abstract syntax tree.
// The v2 pipeline is: lex -> parse (this AST) -> sema -> emit Go.
// v0.1 was a token-rewriting transpiler (transpile.go); v2 is a real
// frontend with a typed AST, so sema can do name resolution, type
// checking, generic enum instantiation, and exhaustiveness checking.

// ---------- nodes ----------

type File struct {
	PkgName string
	Imports []*ImportDecl
	Decls   []Decl
}

// ImportDecl is `import "path"` — the path is a directory relative to the
// importing package's directory (§3: a package is a directory of .gopp
// files; the path's last element is the package qualifier).
type ImportDecl struct {
	Path string
	Line int
}

type Decl interface{ declNode() }

type Field struct {
	Name string // may be "" for unnamed
	Type TypeExpr
	Line int
}

type FuncDecl struct {
	Name    string
	Params  []Field
	Results []Field // 0 or 1 used in practice
	Body    *Block
	Line    int
}

type EnumDecl struct {
	Name       string
	TypeParams []string // e.g. ["T", "E"]; empty = non-generic
	Variants   []Variant
	Line       int
}

type Variant struct {
	Name   string
	Fields []Field
	Line   int
}

// StructDecl is `type Name struct { ... }`. Derives lists @derive names
// attached above the declaration (e.g. ["Debug", "Clone"]).
type StructDecl struct {
	Name    string
	Fields  []Field
	Derives []string
	Line    int
}

func (*FuncDecl) declNode()     {}
func (*EnumDecl) declNode()     {}
func (*StructDecl) declNode()   {}
func (*ComptimeDecl) declNode() {}

// ComptimeDecl is a top-level `comptime { ... }` block: metaprogramming
// (§10). The block runs during sema BEFORE any type resolution, walking
// and rewriting the package's declarations (decls(), .params.add(...),
// gen(...)); it emits no code of its own.
type ComptimeDecl struct {
	Body *Block
	Line int
	Col  int
}

// ---------- types ----------

type TypeExpr interface{ typeNode() }

type IdentType struct {
	Name string // int, string, Status, T ...
	Line int
	Col  int
}

type IndexType struct { // Result[int, string]
	X    TypeExpr
	Args []TypeExpr
	Line int
	Col  int
}

type MapType struct {
	K, V TypeExpr
	Line int
	Col  int
}

type ChanType struct {
	Elem TypeExpr
	Line int
	Col  int
}

type SliceType struct {
	Elem TypeExpr
	Line int
	Col  int
}

type StarType struct {
	X    TypeExpr
	Line int
	Col  int
}

func (*IdentType) typeNode() {}
func (*IndexType) typeNode() {}
func (*MapType) typeNode()   {}
func (*ChanType) typeNode()  {}
func (*SliceType) typeNode() {}
func (*StarType) typeNode()  {}

// ---------- statements ----------

type Stmt interface{ stmtNode() }

type Block struct {
	List []Stmt
	Line int
}

// VarStmt is `var name Type [= init]`. Map types without init are
// auto-instantiated by the emitter (no nil maps in go++).
type VarStmt struct {
	Name string
	Type TypeExpr
	Init Expr // nil = no initializer
	Line int
	Col  int
}

type ExprStmt struct {
	X    Expr
	Line int
	Col  int
}

type AssignStmt struct {
	Lhs  []Expr
	Op   string // ":=", "=", "+=", ...
	Rhs  []Expr
	Line int
	Col  int
}

type IfStmt struct {
	Init Stmt // optional simple statement before ;
	Cond Expr
	Then *Block
	Else Stmt // *Block or *IfStmt or nil
	Line int
	Col  int
}

// ForStmt covers Go's for: for [init]; [cond]; [post] { } and for-range
// is out of v2 scope.
type ForStmt struct {
	Init Stmt
	Cond Expr
	Post Stmt
	Body *Block
	Line int
	Col  int
}

type LoopStmt struct {
	Body *Block
	Line int
	Col  int
}

// ForInStmt is `for x in expr { }` — comptime-only iteration over a
// comptime list (§10); outside a comptime block sema rejects it.
type ForInStmt struct {
	Var  string
	X    Expr
	Body *Block
	Line int
	Col  int
}

type BreakStmt struct {
	Label string // "" = plain break, "loop" = innermost go++ loop
	Line  int
	Col   int
}

type ReturnStmt struct {
	Results []Expr
	Line    int
	Col     int
}

type IncDecStmt struct {
	X    Expr
	Op   string // "++" or "--"
	Line int
	Col  int
}

func (*Block) stmtNode()      {}
func (*VarStmt) stmtNode()    {}
func (*ExprStmt) stmtNode()   {}
func (*AssignStmt) stmtNode() {}
func (*IfStmt) stmtNode()     {}
func (*ForStmt) stmtNode()    {}
func (*ForInStmt) stmtNode()  {}
func (*LoopStmt) stmtNode()   {}
func (*BreakStmt) stmtNode()  {}
func (*ReturnStmt) stmtNode() {}
func (*IncDecStmt) stmtNode() {}

// ---------- expressions ----------

type Expr interface{ exprNode() }

type Ident struct {
	Name string
	Line int
	Col  int
}

type BasicLit struct {
	Kind  tokKind // kInt, kFloat, kString, kRune
	Value string
	Line  int
	Col   int
}

type BinaryExpr struct {
	Op   string
	X, Y Expr
	Line int // operator position
	Col  int
}

type UnaryExpr struct {
	Op   string // -, !, <-
	X    Expr
	Line int
	Col  int
}

type CallExpr struct {
	Fun  Expr
	Args []Expr
	Line int // the ( position
	Col  int
}

type SelectorExpr struct {
	X    Expr
	Sel  string
	Line int // the selector name position
	Col  int
}

// IndexExpr covers a[i] and generic instantiation Result[int, string];
// sema disambiguates by the type of X.
type IndexExpr struct {
	X     Expr
	Index []Expr
	Line  int // the [ position
	Col   int
}

// MakeChanExpr is `chan[T](cap)` / `chan[T]()` in expression position.
type MakeChanExpr struct {
	Elem TypeExpr
	Cap  Expr // nil = unbuffered
	Line int
	Col  int
}

// StructLitExpr is a composite literal: User{ID: 1, Name: "x"} or
// positional User{1, "x"}. A FieldVal with Name == "" is positional.
type StructLitExpr struct {
	Type   TypeExpr
	Fields []FieldVal
	Line   int
	Col    int
}

type FieldVal struct {
	Name  string
	Value Expr
	Line  int
	Col   int
}

// TryExpr is `expr?` — the try operator (spec §7). It is only valid as
// the direct right-hand side of an assignment, var initializer, or as an
// expression statement: the operand must be Result[T, E] and the
// enclosing function must return Result[_, E]; on Err it returns early.
type TryExpr struct {
	X    Expr
	Line int
	Col  int
}

// ComptimeExpr is `comptime expr`: the expression must be constant —
// literals, the prelude duration constants, arithmetic/comparison/logic,
// conversions — and is evaluated at compile time (§10) with fuel and
// overflow checks. The emitter writes the resulting constant.
type ComptimeExpr struct {
	X    Expr
	Line int
	Col  int
}

// MatchExpr is both a statement (wrapped in ExprStmt) and an expression.
// Subject == nil means the subject-less form (channel/boolean arms).
type MatchExpr struct {
	Subject Expr
	Arms    []MatchArm
	Fair    bool
	Line    int
	Col     int
}

func (*Ident) exprNode()         {}
func (*BasicLit) exprNode()      {}
func (*BinaryExpr) exprNode()    {}
func (*UnaryExpr) exprNode()     {}
func (*CallExpr) exprNode()      {}
func (*SelectorExpr) exprNode()  {}
func (*IndexExpr) exprNode()     {}
func (*MakeChanExpr) exprNode()  {}
func (*MatchExpr) exprNode()     {}
func (*StructLitExpr) exprNode() {}
func (*TryExpr) exprNode()       {}
func (*ComptimeExpr) exprNode()  {}

// ---------- match patterns ----------

type Pattern interface{ patNode() }

type WildcardPat struct {
	Line int
	Col  int
}

// IdentPat binds the subject value to Name.
type IdentPat struct {
	Name string
	Line int
	Col  int
}

// LiteralPat matches by equality (0, "x", someExpr).
type LiteralPat struct {
	X    Expr
	Line int
	Col  int
}

// VariantPat destructures an enum variant: Failed(reason), Ok(v).
type VariantPat struct {
	Name     string
	Bindings []string
	Line     int
	Col      int
}

// RecvPat: x := ch.recv()
type RecvPat struct {
	Bind string // "" or "_" = discard
	Chan Expr
	Line int
	Col  int
}

// SendPat: ch.send(v)
type SendPat struct {
	Chan  Expr
	Value Expr
	Line  int
	Col   int
}

// AfterPat: after(d)
type AfterPat struct {
	D    Expr
	Line int
	Col  int
}

// ClosedPat: ch.closed()
type ClosedPat struct {
	Chan Expr
	Line int
	Col  int
}

// BoolPat: if cond (subject-less boolean arm)
type BoolPat struct {
	X    Expr
	Line int
	Col  int
}

type MatchArm struct {
	Pat      Pattern
	Guard    Expr // nil = no guard
	Body     []Stmt
	BodyExpr Expr // non-nil = single-expression arm
	Line     int
	Col      int
}

func (*WildcardPat) patNode() {}
func (*IdentPat) patNode()    {}
func (*LiteralPat) patNode()  {}
func (*VariantPat) patNode()  {}
func (*RecvPat) patNode()     {}
func (*SendPat) patNode()     {}
func (*AfterPat) patNode()    {}
func (*ClosedPat) patNode()   {}
func (*BoolPat) patNode()     {}
