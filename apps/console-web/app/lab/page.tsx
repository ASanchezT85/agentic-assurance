import { Surface } from "@/components/Surface";

export const dynamic = "force-dynamic";

/**
 * Lab (spec section 48.5).
 *
 * The Digital Twin runs and produces experiment records with everything section 40
 * requires. Nothing persists them: POST /v1/simulations and GET /v1/simulations/{id}
 * are in section 46 and no phase has built them, so there is no store for this page
 * to read.
 *
 * The surface exists, says exactly that, and shows how to run an experiment today. A
 * screen full of invented runs would be worse than an honest empty one, and worst of
 * all on the surface that is about reproducibility.
 */
export default function LabPage() {
  return (
    <Surface title="Lab" summary="Reproducible experiments against the Digital Twin.">
      <div style={{ border: "1px solid currentColor", borderRadius: "4px", padding: "1rem" }}>
        <strong>Not available from the console.</strong> The simulator runs and its
        experiment records carry everything needed to rerun an investigation, but
        nothing stores them: <code>POST /v1/simulations</code> and{" "}
        <code>GET /v1/simulations/&#123;id&#125;</code> from spec section 46 have not
        been built.
        <p style={{ marginBottom: 0 }}>
          Showing sample runs here would mean inventing them, which is the one thing a
          surface about reproducibility must not do.
        </p>
      </div>

      <h2 style={{ fontSize: "1rem", marginTop: "2rem" }}>Running one today</h2>
      <pre style={{ fontSize: "0.8rem", whiteSpace: "pre-wrap" }}>
        python -m simulator.engine --seed 42
      </pre>
      <p style={{ opacity: 0.75 }}>
        The seed is required, not defaulted. An unseeded run is not reproducible, and a
        default would make every unseeded run look reproducible until someone compared
        two of them.
      </p>

      <h2 style={{ fontSize: "1rem", marginTop: "2rem" }}>What a record has to carry</h2>
      <p style={{ opacity: 0.75 }}>
        Scenario and version, code commit, random seed, policy bundle, population
        definition hash, market dataset and its hash, and the start and end times. Two
        runs that disagree can then be narrowed to the one input that differed, which
        is the whole purpose of the record (spec section 40).
      </p>
    </Surface>
  );
}
