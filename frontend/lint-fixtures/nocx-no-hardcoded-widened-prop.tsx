// Negative fixture for the widened text-bearing prop set (nocx-dej6.3.16).
// Every literal below is user-visible copy on a prop the checker reads, and
// each one must fail check-i18n-hardcoded.mjs. Keep it failing: it is the
// proof the set is not a decoration. Not part of the app.
export const Form = () => (
  <>
    <CollectionDialog submitLabel="Create" fieldLabel="Name" />
    <Table addLabel="Add header" emptyLabel="No headers yet" />
    <Disclosure summary="Thinking" />
  </>
)
