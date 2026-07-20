import { TopologyMap } from "./TopologyMap";
import "./styles.css";

export default function App() {
  return (
    <div className="app">
      <nav className="nav">
        <span className="logo">
          <span className="mark">◆</span> Atlas
        </span>
        <span className="crumb">
          Consola de arquitectura › <b>Topología global</b>
        </span>
        <span className="region">
          <span className="dot" /> multi-región · on-prem + nube
        </span>
      </nav>
      <main>
        <TopologyMap />
      </main>
    </div>
  );
}
