import NativeKeyBindingsTab from "../components/NativeKeyBindings";
import PricingTable from "../components/PricingTable";

type MappingSection = "native" | "pricing";

export default function Mapping({ section = "native" }: { section?: MappingSection }) {
  return (
    <div className="map-page">
      {section === "native" ? <NativeKeyBindingsTab /> : <PricingTable />}
    </div>
  );
}
