import { render } from "preact";
import "@fontsource/barlow/400.css";
import "@fontsource/barlow/600.css";
import "@fontsource/barlow-semi-condensed/500.css";
import "@fontsource/barlow-semi-condensed/600.css";
import "./style.css";
import { App } from "./App";

render(<App />, document.getElementById("app")!);
