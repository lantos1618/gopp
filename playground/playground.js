// go++ playground: loads the wasm compiler (global goppCompile, registered
// by the compiler's js/wasm build) and wires up the editor. The compiler
// runs entirely in the browser: lex -> parse -> checkImports -> emit; the
// output is Go source plus diagnostics. Nothing is executed.
//
// The example programs below are verbatim copies of the corresponding
// files in the repo's examples/ directory.

"use strict";

const EXAMPLES = [
  {
    name: "hello.gopp — enums, match, channels, Result",
    src: `package main

// go++ v0.1 demo: enums, match (statement + expression), Result, channels.
// Expected output (println goes to stderr):
//   live
//   live
//   recv fired
//   5
//   err: division by zero

enum Status {
    Pending
    Active
    Failed(reason string)
}

func divide(a int, b int) Result[int, string] {
    if b == 0 {
        return Err[int, string]("division by zero")
    }
    return Ok[int, string](a / b)
}

func main() {
    s := Active

    // statement match on an enum
    match s {
        Pending -> println("waiting...")
        Active -> println("live")
        Failed(reason) -> println("dead: " + reason)
    }

    // value-producing match
    msg := match s {
        Pending -> "waiting..."
        Active -> "live"
        Failed(reason) -> "dead: " + reason
    }
    println(msg)

    // channels without arrows + match as select
    ch := chan[Status](1)
    ch.send(s)
    match {
        _ := ch.recv() -> println("recv fired")
        after(1 * second) -> println("timeout")
    }

    // Result + match
    r := divide(10, 2)
    match r {
        Ok(v) -> println(v)
        Err(e) -> println("err: " + e)
    }

    r2 := divide(1, 0)
    match r2 {
        Ok(v) -> println(v)
        Err(e) -> println("err: " + e)
    }
}
`,
  },
  {
    name: "forin.gopp — range over slices, maps, channels",
    src: `package main

// for-in at runtime: range loops over slices, maps, and channels.

func main() {
    // slice: element
    xs := []int{10, 20, 30}
    total := 0
    for x in xs {
        total += x
    }
    println(total)

    // slice: index + element
    for i, x in xs {
        println(i, x)
    }

    // map: value, then key + value
    var m map<string, int>
    m["a"] = 1
    m["b"] = 2
    sum := 0
    for v in m {
        sum += v
    }
    println(sum)
    keys := 0
    for k, v in m {
        if k == "a" && v == 1 {
            keys++
        }
        if k == "b" && v == 2 {
            keys++
        }
    }
    println(keys)

    // chan: receive until closed
    ch := chan[int](2)
    ch.send(7)
    ch.send(8)
    ch.close()
    got := 0
    for n in ch {
        got += n
    }
    println(got)

    // break works like any loop
    count := 0
    for x in xs {
        if x > 15 {
            break
        }
        count++
    }
    println(count)
}
`,
  },
  {
    name: "generic.gopp — generic functions and enums",
    src: `package main

// §8: generic functions. Bodies are checked ONCE against rigid type
// parameters; call sites instantiate by inference (from the expected
// type and the value arguments) or by explicit type arguments.

enum Pair[A, B] {
    Pair(a A, b B)
}

func Identity[T](x T) T {
    return x
}

func First[A, B](a A, b B) A {
    return a
}

func Wrap[T](x T) Option[T] {
    return Some[T](x)
}

func UnwrapOr[T](o Option[T], fallback T) T {
    return match o {
        Some(v) -> v
        None -> fallback
    }
}

func MakePair[A, B](a A, b B) Pair[A, B] {
    return Pair[A, B](a, b)
}

func Swap[A, B](p Pair[A, B]) Pair[B, A] {
    return match p {
        Pair(a, b) -> Pair[B, A](b, a)
    }
}

func main() {
    println(Identity(42))         // T = int, inferred from the argument
    println(Identity("go++"))     // T = string
    println(Identity[bool](true)) // explicit type argument
    println(First(1, "one"))
    println(UnwrapOr(Wrap(7), 0)) // nested inference: T = int twice
    println(UnwrapOr(None[int](), 9))
    s := Swap(MakePair(1, "one"))
    match s {
        Pair(b, a) -> println(b, a)
    }
}
`,
  },
  {
    name: "interp.gopp — string interpolation",
    src: `package main

// string interpolation: {expr} inside strings, {{ and }} for literals.

func main() {
    name := "gopher"
    n := 42
    println("hi {name}!")
    println("n = {n}, n+1 = {n + 1}")
    println("float: {3.5}, bool: {n > 0}")
    println("literal braces: {{}}")
    xs := []int{1, 2}
    println("len: {len(xs)}, first: {xs[0]}")
    p := Point{X: 3}
    println("field: {p.X}")
    println("call: {double(21)}")
    d := 100 * ms
    println("duration: {d}")
    m := map<string, int>{"answer": 42}
    println("nested quotes: {m["answer"]}")
}

type Point struct {
    X int
}

func double(n int) int {
    return n * 2
}
`,
  },
];

const srcEl = document.getElementById("src");
const diagsEl = document.getElementById("diags");
const outEl = document.getElementById("out");
const btn = document.getElementById("compile");
const statusEl = document.getElementById("status");
const selectEl = document.getElementById("examples");

for (const ex of EXAMPLES) {
  const opt = document.createElement("option");
  opt.value = ex.name;
  opt.textContent = ex.name;
  selectEl.appendChild(opt);
}
selectEl.addEventListener("change", () => {
  const ex = EXAMPLES.find((e) => e.name === selectEl.value);
  if (ex) {
    srcEl.value = ex.src;
    compile();
  }
});

// preload with the hello-world example
selectEl.value = EXAMPLES[0].name;
srcEl.value = EXAMPLES[0].src;

function compile() {
  if (typeof goppCompile !== "function") {
    return; // compiler not ready yet
  }
  let res;
  try {
    res = goppCompile(srcEl.value);
  } catch (e) {
    diagsEl.className = "err";
    diagsEl.textContent = "compiler crashed: " + e;
    outEl.textContent = "";
    return;
  }
  const ok = res.go !== "";
  diagsEl.className = ok ? "ok" : "err";
  diagsEl.textContent = res.diags !== "" ? res.diags : "ok — no diagnostics";
  outEl.textContent = res.go;
}

btn.addEventListener("click", compile);
srcEl.addEventListener("keydown", (e) => {
  if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
    e.preventDefault();
    compile();
  }
});

// Boot the wasm compiler. goppCompile is registered during the module's
// init; poll briefly until it appears, then enable the UI.
async function boot() {
  const go = new Go();
  const result = await WebAssembly.instantiateStreaming(
    fetch("gopp.wasm"),
    go.importObject
  );
  go.run(result.instance); // never returns: the wasm main blocks forever
  for (let i = 0; i < 200 && typeof goppCompile !== "function"; i++) {
    await new Promise((r) => setTimeout(r, 25));
  }
  if (typeof goppCompile !== "function") {
    statusEl.textContent = "compiler failed to initialize";
    return;
  }
  statusEl.textContent = "";
  btn.disabled = false;
  compile();
}

boot().catch((e) => {
  statusEl.textContent = "failed to load gopp.wasm: " + e;
});
