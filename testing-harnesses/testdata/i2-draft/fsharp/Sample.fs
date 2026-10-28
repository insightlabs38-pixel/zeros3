namespace Q9

type State = Ready | Busy of int
module Work =
    let solve value = match value with Ready -> 1 | Busy n -> n + 3
    let names = [| for i in 0..255 -> sprintf "item-%04d" i |]
