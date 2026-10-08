# The encoding-reflection class

This page states what the `encoding-reflection` exemption class of deadset-go retains, for a maintainer reading why an encoded or reflected type kept a member. A type whose values reach a consumer that names members by string at run time keeps the members that consumer reads. Those are the exported fields, every field carrying a struct tag, and the methods the consumer resolves by name.

The destinations, and what each retains:

- Fields and the methods it resolves by name for an argument of a function or method of `encoding/json`, `encoding/json/v2`, `encoding/xml` or `encoding/gob`. A decoding entry point fills fields through reflection, which reads none, so it retains its methods alone. An encoder skips a field whose tag for that encoder is exactly `-`, and an XML decoding entry point reads the `XMLName` field. A JSON decoding entry point retains fields as well in a program that calls `(*json.Decoder).DisallowUnknownFields` or names `json.RejectUnknownMembers`.
- Fields alone for a `database/sql` scan target and an argument of `reflect.DeepEqual`. Fields alone also for an argument of any other `reflect` function in a program that calls none of the method finders `Method`, `MethodByName` and `NumMethod`.
- Fields and the `LogValue` method for a `log/slog` logging call or attribute constructor.
- Fields and the exported methods for a template engine and a sort interface. The same holds for an argument of any other function of `reflect` in a program that calls one of those method finders.
- For a destination outside the analyzed program, the members [a value that leaves the analysis](exemptions.md#what-leaves-the-analysis-is-fully-reachable) keeps.

An encoder resolves a method by name in the direction its entry point works in. An entry point whose name begins `Encode` or `Marshal` retains the encoding methods. One whose name begins `Decode` or `Unmarshal` retains the decoding methods. One whose name begins with neither retains both, because it takes a value for either direction.

| Package | Encoding | Decoding |
| --- | --- | --- |
| `encoding/json`, `encoding/json/v2` | `AppendText`, `MarshalJSON`, `MarshalJSONTo`, `MarshalText` | `UnmarshalJSON`, `UnmarshalJSONFrom`, `UnmarshalText` |
| `encoding/xml` | `MarshalText`, `MarshalXML`, `MarshalXMLAttr` | `UnmarshalText`, `UnmarshalXML`, `UnmarshalXMLAttr` |
| `encoding/gob` | `GobEncode`, `MarshalBinary` | `GobDecode`, `UnmarshalBinary` |

A value reaches through a pointer, a slice, an array, a map key or value and an embedded field. It also reaches through the fields of a struct type with no name. From every type so reached, it reaches through that type's fields again until no further type joins, because an encoder walks the whole value rather than its outermost type.

A field that is an interface, or holds interface elements or map values, reaches the types the program stores in it. A store is a composite literal, an assignment, an `append` or an index assignment. A stored parameter stands for every argument the program's calls pass for it. An argument holding interface elements or map values reaches the stored types the same way. It may be a literal, a variable, a parameter or a field selector.

A method is retained where the defined type declares it, so a method promoted from an embedded type is retained where the embedded type is reached.
