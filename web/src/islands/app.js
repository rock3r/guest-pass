import { render } from "preact";
import { useState } from "preact/hooks";
import "../styles/tokens.css";

/**
 * Counter is a throwaway SPIKE-1 island proving the npm-free toolchain end to end:
 * vendored Preact + hooks + automatic JSX (authored in plain .js, D-32). Real
 * islands (device-check, guest-session, greenroom) replace this in M2/M3.
 *
 * @returns {import("preact").VNode}
 */
function Counter() {
  const [n, setN] = useState(0);
  return (
    <button class="gp-probe" onClick={() => setN(n + 1)}>
      clicked {n} {n === 1 ? "time" : "times"}
    </button>
  );
}

const root = document.getElementById("app");
if (root) render(<Counter />, root);
