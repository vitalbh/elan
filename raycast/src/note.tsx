import { LaunchProps, showHUD, showToast, Toast } from "@raycast/api";
import { elanText } from "./elan";

export default async function Command(props: LaunchProps<{ arguments: { text: string } }>) {
  const text = props.arguments.text.trim();
  if (!text) return;
  try {
    await elanText("note", text);
    await showHUD("Anotado no elan");
  } catch (e) {
    await showToast({ style: Toast.Style.Failure, title: "Não anotou", message: (e as Error).message });
  }
}
