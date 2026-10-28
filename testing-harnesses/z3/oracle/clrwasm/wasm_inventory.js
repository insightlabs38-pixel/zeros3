// ORACLE (Z3-33): V8's WebAssembly implementation validates each module and reports its import,
// export and custom-section inventory; `z3wasm oracle` compares those with wasmz3's parse.
// Usage: node wasm_inventory.js FILE...
const fs = require('fs');
for (const p of process.argv.slice(2)) {
  const b = fs.readFileSync(p);
  let ok = false, imp = [], exp = [], cus = [];
  try {
    ok = WebAssembly.validate(b);
    if (ok) {
      const m = new WebAssembly.Module(b);
      imp = WebAssembly.Module.imports(m); exp = WebAssembly.Module.exports(m);
      cus = WebAssembly.Module.customSections(m, 'name').length;
    }
  } catch (e) { ok = false; }
  const kinds = k => k.reduce((a, x) => { a[x.kind] = (a[x.kind] || 0) + 1; return a; }, {});
  console.log(`${p}\t${ok}\timports=${JSON.stringify(kinds(imp))}\texports=${JSON.stringify(kinds(exp))}`);
}
